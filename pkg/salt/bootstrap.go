package salt

import (
	"fmt"
	"strings"

	"github.com/stefanob/terraform-provider-salt/pkg/ssh"
)

// EnsureVersion checks if the desired Salt version is installed on the remote
// host and bootstraps it if not.
//
// Special values:
//   - "latest": ensures Salt is installed (any version), bootstraps if missing.
//   - A version number like "3007": ensures that specific version is present.
func EnsureVersion(client *ssh.Client, version string) error {
	out, _ := client.Run("salt-call --version 2>/dev/null || echo 'not-installed'")

	if version == "latest" {
		// Just need Salt to be present, any version
		if !strings.Contains(out, "not-installed") && strings.Contains(out, "salt-call") {
			return nil
		}
		return bootstrap(client, "")
	}

	if strings.Contains(out, version) {
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
