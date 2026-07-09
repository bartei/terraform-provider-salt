package salt

import (
	"fmt"
	"strings"

	"github.com/bartei/terraform-provider-salt/pkg/ssh"
)

// saltCallPath is the extended PATH used to find salt-call across common
// install locations (onedir, pip, system package).
const saltPath = "/opt/saltstack/salt/bin:/usr/local/bin:/usr/bin:/bin"

// SaltCallCmd returns a sudo invocation of salt-call with the correct PATH
// so it works regardless of where Salt was installed.
func SaltCallCmd() string {
	return fmt.Sprintf(`sudo env "PATH=%s:$PATH" salt-call`, saltPath)
}

// FindSaltCall returns the version string if salt-call is installed, or empty string if not.
//
// Uses sudo to match the actual salt-call invocation pattern (SaltCallCmd).
// Without sudo, hosts where the salt onedir is root-only (or where any
// component of /opt/saltstack/salt is mode 0700) report salt-call as
// missing, causing EnsureVersion to re-bootstrap on every apply.
func FindSaltCall(client *ssh.Client) string {
	cmd := fmt.Sprintf(`%s --version 2>/dev/null || echo 'not-installed'`, SaltCallCmd())
	out, _ := client.Run(cmd)
	if strings.Contains(out, "not-installed") {
		return ""
	}
	return strings.TrimSpace(out)
}

// EnsureVersion checks if the desired Salt version is installed on the remote
// host and bootstraps it if not.
//
// Special values:
//   - "latest": ensures Salt is installed (any version), bootstraps if missing.
//   - A version like "3008" or "3008.2": ensures that feature release is
//     present. Matching is by feature release (see matchesFeatureRelease), so a
//     patch update within the release (e.g. 3008.2 -> 3008.7, which an OS
//     package upgrade applies from the same Salt repo) does NOT break apply.
//
// When a *different feature release* is already installed we fail loudly rather
// than re-bootstrap: salt-bootstrap's re-install path is unreliable across
// distros (e.g. on Fedora it ignores the version argument when
// /etc/yum.repos.d/salt.repo already exists, and its post-install service check
// can fail when salt-minion is masked), and a feature-release change can alter
// behavior (Argon, for one, masks pillar output by default). Failing loudly
// there gives the operator a clear signal and avoids silent drift.
// Concurrency: callers must hold HostLockFor(client.Host) when invoking
// EnsureVersion so that bootstrap and the salt-call ops that follow all
// run serialized per host. EnsureVersion itself is *not* internally locked
// because the resource layer needs to hold the same lock through Apply/
// Test, and a second Lock() here would deadlock.
func EnsureVersion(client *ssh.Client, version string) error {
	installed := FindSaltCall(client)

	if version == "latest" {
		if installed != "" {
			return nil
		}
		return bootstrap(client, "")
	}

	if installed == "" {
		if err := bootstrap(client, version); err != nil {
			return err
		}
		// Verify the install produced the requested feature release.
		// salt-bootstrap on some distros (notably Fedora) silently installs a
		// different version when the requested one isn't in its repo. Matching
		// on feature release means a patch difference within the release is fine.
		got := FindSaltCall(client)
		if !matchesFeatureRelease(got, version) {
			return fmt.Errorf(
				"bootstrap installed Salt version %q but %q was requested — "+
					"salt-bootstrap does not appear to have a build of %q for this distro. "+
					"Pin salt_version to a version your distro supports, or use \"latest\"",
				strings.TrimSpace(got), version, version)
		}
		return nil
	}

	// Match on feature release so routine OS package updates that bump the patch
	// within a release (e.g. 3008.2 -> 3008.7, from the same Salt repo) don't
	// break apply. A feature-release change (e.g. 3008 -> 3009) can alter
	// behavior, so we still fail loudly there rather than silently adopt it.
	if matchesFeatureRelease(installed, version) {
		return nil
	}

	return fmt.Errorf(
		"installed Salt %q is a different feature release than requested %q on the target host. "+
			"A patch update within the release is tolerated, but this is a feature-release change "+
			"that can alter behavior; this provider does not perform cross-release upgrades — "+
			"uninstall Salt and re-apply, or set salt_version to the installed release",
		strings.TrimSpace(installed), version)
}

// matchesFeatureRelease reports whether an installed Salt version string and the
// requested salt_version share a feature release. "latest" always matches.
func matchesFeatureRelease(installed, requested string) bool {
	if requested == "latest" {
		return true
	}
	ir := saltFeatureRelease(installed)
	return ir != "" && ir == saltFeatureRelease(requested)
}

// saltFeatureRelease extracts Salt's feature-release number — the first run of
// digits — from a version string ("3008" from "3008.2", "3008", or
// "salt-call 3008.2 (Argon)"). Returns "" when there are no digits.
func saltFeatureRelease(s string) string {
	start := -1
	for i := 0; i < len(s); i++ {
		if s[i] >= '0' && s[i] <= '9' {
			if start == -1 {
				start = i
			}
		} else if start != -1 {
			return s[start:i]
		}
	}
	if start != -1 {
		return s[start:]
	}
	return ""
}

// bootstrapScriptURL is the salt-bootstrap installer we fetch and run.
const bootstrapScriptURL = "https://github.com/saltstack/salt-bootstrap/releases/latest/download/bootstrap-salt.sh"

// bootstrapScriptPath is where we stage the installer on the remote host.
const bootstrapScriptPath = "/tmp/bootstrap-salt.sh"

func bootstrap(client *ssh.Client, version string) error {
	versionArg := ""
	if version != "" {
		versionArg = " stable " + version
	}

	// Fetch the installer to a file, then run it — do NOT `curl ... | sh`.
	// On minimal images (e.g. the Ubuntu LXC template) curl is absent, and a
	// piped `curl | sh` silently no-ops there: the missing curl produces an
	// empty stdin, sh exits 0, and Salt is never installed — surfacing much
	// later as an opaque "salt-call: not found" (exit 127). Staging to a file
	// under `set -e` makes a failed download a hard, obvious error instead.
	//
	// Downloader selection is deliberately tolerant: prefer curl, fall back to
	// wget (present on many minimal images even when curl isn't), and only if
	// neither exists install curl via the distro package manager. The
	// salt-bootstrap script itself also needs curl/wget/fetch to pull the Salt
	// packages, so guaranteeing one exists fixes both stages.
	if _, err := client.Run(fetchScriptCmd(bootstrapScriptURL, bootstrapScriptPath)); err != nil {
		return fmt.Errorf("fetching salt-bootstrap script: %w", err)
	}

	// -X tells the bootstrap script not to start daemons after installation.
	// We run masterless (salt-call --local), so the salt-minion service is
	// never wanted — leaving it running just wastes time hanging on DNS
	// lookups for the default master hostname 'salt'.
	bootstrapCmd := fmt.Sprintf(`sudo sh %s -X -P -x python3%s`, bootstrapScriptPath, versionArg)
	if _, err := client.Run(bootstrapCmd); err != nil {
		return fmt.Errorf("bootstrap failed: %w", err)
	}

	// Belt-and-suspenders: even with -X, package postinst hooks on some
	// distros may have already started the minion, and it may be stuck in
	// a 30s DNS-retry loop. SIGKILL skips the graceful shutdown, then we
	// mask the unit so nothing can re-enable it. All steps are idempotent
	// and tolerate an absent unit.
	disableMinion(client)

	// Verify the install actually produced a usable salt-call. salt-bootstrap
	// can exit 0 while leaving nothing runnable (a missing/mismatched build,
	// an interrupted download), so confirm here and fail loudly rather than
	// letting the next salt-call surface an opaque code 127.
	if FindSaltCall(client) == "" {
		return fmt.Errorf("salt-bootstrap completed but salt-call is still not available on %s "+
			"(check the host has internet egress and a working package manager)", client.Host)
	}

	return nil
}

// fetchScriptCmd builds a POSIX-sh command that downloads url to path using
// whatever downloader the host has (curl or wget), installing curl via the
// distro package manager as a last resort. `set -e` ensures any failing step
// aborts with a non-zero exit so the caller sees a real error.
func fetchScriptCmd(url, path string) string {
	return fmt.Sprintf(`set -e
if command -v curl >/dev/null 2>&1; then
  curl -fsSL -o %[1]s %[2]s
elif command -v wget >/dev/null 2>&1; then
  wget -qO %[1]s %[2]s
else
  if command -v apt-get >/dev/null 2>&1; then sudo apt-get update -qq && sudo apt-get install -y curl
  elif command -v dnf >/dev/null 2>&1; then sudo dnf install -y curl
  elif command -v yum >/dev/null 2>&1; then sudo yum install -y curl
  elif command -v zypper >/dev/null 2>&1; then sudo zypper --non-interactive install curl
  elif command -v apk >/dev/null 2>&1; then sudo apk add --no-cache curl
  else echo "no curl/wget and no supported package manager to install one" >&2; exit 1
  fi
  curl -fsSL -o %[1]s %[2]s
fi`, path, url)
}

// disableMinion stops, disables, and masks the salt-minion service on the
// remote host. The provider runs masterless via salt-call --local; the
// minion daemon is never used.
//
// Uses SIGKILL rather than a graceful stop because a freshly installed
// minion is typically stuck in a 30-second DNS retry loop trying to reach
// the default master hostname 'salt' — graceful stop would wait for that
// loop to finish.
func disableMinion(client *ssh.Client) {
	cmd := `sudo systemctl mask salt-minion 2>/dev/null || true; ` +
		`sudo systemctl kill -s KILL salt-minion 2>/dev/null || true; ` +
		`sudo systemctl disable salt-minion 2>/dev/null || true`
	_, _ = client.Run(cmd)
}
