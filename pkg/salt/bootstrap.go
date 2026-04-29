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
//   - A version number like "3007": ensures that specific version is present.
//
// When a specific version is requested but a different version is already
// installed, we fail loudly rather than try to re-bootstrap. salt-bootstrap's
// re-install path is unreliable across distros (e.g. on Fedora it ignores
// the version argument when /etc/yum.repos.d/salt.repo already exists, and
// its post-install service check can fail when salt-minion is masked).
// Failing loudly gives the operator a clear signal and avoids silent drift.
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
		// Verify the install actually produced the requested version.
		// salt-bootstrap on some distros (notably Fedora) silently installs
		// a different version when the requested one isn't in its repo.
		got := FindSaltCall(client)
		if !strings.Contains(got, version) {
			return fmt.Errorf(
				"bootstrap installed Salt version %q but %q was requested — "+
					"salt-bootstrap does not appear to have a build of %q for this distro. "+
					"Pin salt_version to a version your distro supports, or use \"latest\"",
				strings.TrimSpace(got), version, version)
		}
		return nil
	}

	if strings.Contains(installed, version) {
		return nil
	}

	return fmt.Errorf(
		"installed Salt version %q does not match requested %q on the target host. "+
			"This provider does not perform in-place upgrades — uninstall Salt manually "+
			"and re-apply, or change salt_version to match what is installed",
		strings.TrimSpace(installed), version)
}

func bootstrap(client *ssh.Client, version string) error {
	versionArg := ""
	if version != "" {
		versionArg = " stable " + version
	}

	// -X tells the bootstrap script not to start daemons after installation.
	// We run masterless (salt-call --local), so the salt-minion service is
	// never wanted — leaving it running just wastes time hanging on DNS
	// lookups for the default master hostname 'salt'.
	bootstrapCmd := fmt.Sprintf(
		`curl -fsSL https://github.com/saltstack/salt-bootstrap/releases/latest/download/bootstrap-salt.sh | sudo sh -s -- -X -P -x python3%s`,
		versionArg,
	)

	if _, err := client.Run(bootstrapCmd); err != nil {
		return fmt.Errorf("bootstrap failed: %w", err)
	}

	// Belt-and-suspenders: even with -X, package postinst hooks on some
	// distros may have already started the minion, and it may be stuck in
	// a 30s DNS-retry loop. SIGKILL skips the graceful shutdown, then we
	// mask the unit so nothing can re-enable it. All steps are idempotent
	// and tolerate an absent unit.
	disableMinion(client)

	return nil
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
