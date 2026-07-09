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

func TestSaltFeatureRelease(t *testing.T) {
	cases := map[string]string{
		"3008.2":                    "3008",
		"3008":                      "3008",
		"3007.14":                   "3007",
		"salt-call 3008.2 (Argon)":  "3008",
		"salt-call 3007.14 (Chlor)": "3007",
		"latest":                    "",
		"":                          "",
	}
	for in, want := range cases {
		if got := saltFeatureRelease(in); got != want {
			t.Errorf("saltFeatureRelease(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestMatchesFeatureRelease is the core robustness guard: an OS package update
// that bumps the patch within a feature release must NOT be treated as a
// mismatch, while a feature-release change still must be.
func TestMatchesFeatureRelease(t *testing.T) {
	cases := []struct {
		installed, requested string
		want                 bool
		why                  string
	}{
		{"salt-call 3008.7 (Argon)", "3008.2", true, "patch update within release — must tolerate (the OS-update case)"},
		{"salt-call 3008.2 (Argon)", "3008.2", true, "exact match"},
		{"salt-call 3008.2 (Argon)", "3008", true, "release-only pin matches any patch"},
		{"salt-call 3009.0 (Bismuth)", "3008.2", false, "feature-release jump must be flagged"},
		{"salt-call 3007.14 (Chlorine)", "3008", false, "different release"},
		{"salt-call 3008.2 (Argon)", "latest", true, `"latest" always matches`},
		{"", "3008.2", false, "not installed"},
	}
	for _, c := range cases {
		if got := matchesFeatureRelease(c.installed, c.requested); got != c.want {
			t.Errorf("matchesFeatureRelease(%q, %q) = %v, want %v (%s)",
				c.installed, c.requested, got, c.want, c.why)
		}
	}
}
