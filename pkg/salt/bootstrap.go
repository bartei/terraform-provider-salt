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
func FindSaltCall(client *ssh.Client) string {
	cmd := fmt.Sprintf(`env "PATH=%s:$PATH" salt-call --version 2>/dev/null || echo 'not-installed'`, saltPath)
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
func EnsureVersion(client *ssh.Client, version string) error {
	installed := FindSaltCall(client)

	if version == "latest" {
		if installed != "" {
			return nil
		}
		return bootstrap(client, "")
	}

	if strings.Contains(installed, version) {
		return nil
	}

	return bootstrap(client, version)
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
