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

	bootstrapCmd := fmt.Sprintf(
		`curl -fsSL https://github.com/saltstack/salt-bootstrap/releases/latest/download/bootstrap-salt.sh | sudo sh -s -- -P -x python3%s`,
		versionArg,
	)

	if _, err := client.Run(bootstrapCmd); err != nil {
		return fmt.Errorf("bootstrap failed: %w", err)
	}

	// Disable salt-minion daemon — we only want masterless mode
	_, _ = client.Run("systemctl disable --now salt-minion 2>/dev/null || true")

	return nil
}
