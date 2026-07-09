package salt

import (
	"strings"
	"testing"
)

// TestFetchScriptCmd guards the minimal-image regression: the installer must be
// fetched to a file (never `curl | sh`, which silently no-ops when curl is
// absent) and must fall back from curl to wget before trying to install one.
func TestFetchScriptCmd(t *testing.T) {
	const url = "https://example.test/bootstrap-salt.sh"
	const path = "/tmp/bootstrap-salt.sh"
	cmd := fetchScriptCmd(url, path)

	// No piping the download straight into a shell — that's the silent-failure
	// bug (empty pipe → sh exits 0 → Salt never installs).
	for _, bad := range []string{"| sh", "|sh", "| sudo sh", "|sudo sh"} {
		if strings.Contains(cmd, bad) {
			t.Fatalf("fetch command pipes into a shell (%q); download must go to a file:\n%s", bad, cmd)
		}
	}

	// Any failing step must abort.
	if !strings.HasPrefix(cmd, "set -e") {
		t.Errorf("fetch command should start with `set -e`:\n%s", cmd)
	}

	// Prefer curl, fall back to wget, then package-manager install as last resort.
	for _, want := range []string{
		"command -v curl", "curl -fsSL",
		"command -v wget", "wget -qO",
		"apt-get", "dnf", "apk",
	} {
		if !strings.Contains(cmd, want) {
			t.Errorf("fetch command missing %q:\n%s", want, cmd)
		}
	}

	// The url and destination path must both be interpolated in.
	if !strings.Contains(cmd, url) {
		t.Errorf("fetch command missing url %q:\n%s", url, cmd)
	}
	if !strings.Contains(cmd, path) {
		t.Errorf("fetch command missing path %q:\n%s", path, cmd)
	}
}
