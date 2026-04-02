package salt

import (
	"strings"
	"testing"
)

func TestParseResult_Success(t *testing.T) {
	raw := `{
		"local": {
			"file_|-create_file_|-/tmp/test_|-managed": {
				"name": "/tmp/test",
				"result": true,
				"comment": "File /tmp/test is in the correct state",
				"changes": {},
				"__sls__": "test"
			}
		}
	}`

	result, err := parseResult(raw)
	if err != nil {
		t.Fatalf("parseResult failed: %v", err)
	}
	if !result.Success {
		t.Errorf("expected Success=true")
	}
	if !result.InSync {
		t.Errorf("expected InSync=true (no changes)")
	}
	if len(result.Changes) != 1 {
		t.Errorf("expected 1 state, got %d", len(result.Changes))
	}
	if result.Hash == "" {
		t.Errorf("expected non-empty hash")
	}
	if result.RawJSON != raw {
		t.Errorf("RawJSON should be preserved")
	}

	// Verify SLS and ID are captured
	for _, sr := range result.Changes {
		if sr.SLS != "test" {
			t.Errorf("expected SLS=test, got %q", sr.SLS)
		}
		if sr.ID == "" {
			t.Errorf("expected non-empty ID")
		}
	}
}

func TestParseResult_WithChanges(t *testing.T) {
	raw := `{
		"local": {
			"file_|-create_file_|-/tmp/test_|-managed": {
				"name": "/tmp/test",
				"result": true,
				"comment": "File /tmp/test updated",
				"changes": {"diff": "--- old\n+++ new\n"},
				"__sls__": "test"
			}
		}
	}`

	result, err := parseResult(raw)
	if err != nil {
		t.Fatalf("parseResult failed: %v", err)
	}
	if !result.Success {
		t.Errorf("expected Success=true")
	}
	if result.InSync {
		t.Errorf("expected InSync=false when changes present")
	}
}

func TestParseResult_Failure(t *testing.T) {
	raw := `{
		"local": {
			"file_|-create_file_|-/tmp/test_|-managed": {
				"name": "/tmp/test",
				"result": false,
				"comment": "Permission denied",
				"changes": {},
				"__sls__": "test"
			}
		}
	}`

	result, err := parseResult(raw)
	if err != nil {
		t.Fatalf("parseResult failed: %v", err)
	}
	if result.Success {
		t.Errorf("expected Success=false")
	}
}

func TestParseResult_MultipleStates(t *testing.T) {
	raw := `{
		"local": {
			"file_|-a_|-/tmp/a_|-managed": {
				"name": "/tmp/a",
				"result": true,
				"comment": "ok",
				"changes": {},
				"__sls__": "files"
			},
			"pkg_|-b_|-nginx_|-installed": {
				"name": "nginx",
				"result": false,
				"comment": "Package nginx failed to install",
				"changes": {},
				"__sls__": "webserver"
			}
		}
	}`

	result, err := parseResult(raw)
	if err != nil {
		t.Fatalf("parseResult failed: %v", err)
	}
	if result.Success {
		t.Errorf("expected Success=false when any state fails")
	}
	if len(result.Changes) != 2 {
		t.Errorf("expected 2 states, got %d", len(result.Changes))
	}
}

func TestParseResult_MissingLocalKey(t *testing.T) {
	raw := `{"other": {}}`
	_, err := parseResult(raw)
	if err == nil {
		t.Fatal("expected error for missing 'local' key")
	}
	if !strings.Contains(err.Error(), "missing 'local' key") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestParseResult_InvalidJSON(t *testing.T) {
	_, err := parseResult("not json")
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
	if !strings.Contains(err.Error(), "Raw output") {
		t.Errorf("expected raw output in error, got: %v", err)
	}
}

func TestParseResult_EmptyLocal(t *testing.T) {
	raw := `{"local": {}}`
	result, err := parseResult(raw)
	if err != nil {
		t.Fatalf("parseResult failed: %v", err)
	}
	if !result.Success {
		t.Errorf("expected Success=true for empty states")
	}
	if !result.InSync {
		t.Errorf("expected InSync=true for empty states")
	}
}

func TestParseResult_ErrorArray(t *testing.T) {
	// Salt returns errors as {"local": ["error message"]}
	raw := `{"local": ["Rendering SLS 'base:test' failed: Jinja variable 'foo' is undefined"]}`
	_, err := parseResult(raw)
	if err == nil {
		t.Fatal("expected error for error array response")
	}
	if !strings.Contains(err.Error(), "Jinja variable") {
		t.Errorf("expected Jinja error in message, got: %v", err)
	}
	if !strings.Contains(err.Error(), "salt returned an error") {
		t.Errorf("expected 'salt returned an error' prefix, got: %v", err)
	}
}

func TestParseResult_ErrorArrayMultiple(t *testing.T) {
	raw := `{"local": ["Error one", "Error two"]}`
	_, err := parseResult(raw)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "Error one") || !strings.Contains(err.Error(), "Error two") {
		t.Errorf("expected both errors in message, got: %v", err)
	}
}

func TestFailedStates(t *testing.T) {
	result := &Result{
		Changes: map[string]StateResult{
			"state_a": {ID: "state_a", Name: "file_a", SLS: "files", Result: true, Comment: "ok"},
			"state_b": {ID: "state_b", Name: "file_b", SLS: "files", Result: false, Comment: "Permission denied"},
			"state_c": {ID: "state_c", Name: "file_c", SLS: "test", Result: false, Comment: "File not found"},
		},
	}

	out := result.FailedStates()
	if !strings.Contains(out, "Permission denied") {
		t.Errorf("expected Permission denied in output, got:\n%s", out)
	}
	if !strings.Contains(out, "File not found") {
		t.Errorf("expected File not found in output, got:\n%s", out)
	}
	if !strings.Contains(out, "file_b") {
		t.Errorf("expected state name file_b in output, got:\n%s", out)
	}
	if !strings.Contains(out, "sls: files") {
		t.Errorf("expected SLS info in output, got:\n%s", out)
	}
	if strings.Contains(out, "file_a") {
		t.Errorf("successful state_a should not appear in FailedStates, got:\n%s", out)
	}
}

func TestFailedStates_WithStderr(t *testing.T) {
	result := &Result{
		Changes: map[string]StateResult{
			"state_a": {ID: "state_a", Name: "pkg", Result: false, Comment: "install failed"},
		},
		Stderr: "[WARNING ] Some salt warning\n",
	}

	out := result.FailedStates()
	if !strings.Contains(out, "install failed") {
		t.Errorf("expected failure comment, got:\n%s", out)
	}
	if !strings.Contains(out, "Some salt warning") {
		t.Errorf("expected stderr in output, got:\n%s", out)
	}
}

func TestFailedStates_None(t *testing.T) {
	result := &Result{
		Changes: map[string]StateResult{
			"state_a": {ID: "state_a", Name: "file_a", Result: true, Comment: "ok"},
		},
	}
	if out := result.FailedStates(); out != "" {
		t.Errorf("expected empty string, got: %s", out)
	}
}

func TestSummary(t *testing.T) {
	result := &Result{
		Changes: map[string]StateResult{
			"state_a": {ID: "state_a", Name: "/tmp/a", SLS: "test", Result: true, Comment: "ok", Changes: nil},
			"state_b": {ID: "state_b", Name: "/tmp/b", SLS: "test", Result: true, Comment: "updated", Changes: map[string]interface{}{"diff": "..."}},
			"state_c": {ID: "state_c", Name: "nginx", SLS: "web", Result: false, Comment: "not found"},
		},
	}

	out := result.Summary()
	// Failed should come first
	failedIdx := strings.Index(out, "Failed")
	changedIdx := strings.Index(out, "Changed")
	unchangedIdx := strings.Index(out, "Unchanged")

	if failedIdx == -1 || changedIdx == -1 || unchangedIdx == -1 {
		t.Fatalf("expected all three sections, got:\n%s", out)
	}
	if failedIdx > changedIdx || changedIdx > unchangedIdx {
		t.Errorf("expected Failed < Changed < Unchanged order, got:\n%s", out)
	}
	if !strings.Contains(out, "nginx") {
		t.Errorf("expected failed state name, got:\n%s", out)
	}
}

func TestCleanStderr(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		contains string
		excludes string
	}{
		{
			name:     "filters log file warning",
			input:    "[WARNING ] Failed to open log file, do you have permission?\n[ERROR ] Real error here",
			contains: "Real error here",
			excludes: "Failed to open log file",
		},
		{
			name:     "keeps real warnings",
			input:    "[WARNING ] Deprecation notice: use new syntax",
			contains: "Deprecation notice",
		},
		{
			name:  "empty after filtering",
			input: "[WARNING ] Failed to open log file, do you have permission?\n\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := CleanStderr(tt.input)
			if tt.contains != "" && !strings.Contains(got, tt.contains) {
				t.Errorf("expected %q in output, got: %q", tt.contains, got)
			}
			if tt.excludes != "" && strings.Contains(got, tt.excludes) {
				t.Errorf("expected %q to be filtered out, got: %q", tt.excludes, got)
			}
		})
	}
}

func TestGenerateTop_SingleState(t *testing.T) {
	states := map[string]string{
		"webserver.sls": "nginx:\n  pkg.installed\n",
	}
	got := generateTop(states)
	expected := "base:\n  '*':\n    - webserver\n"
	if got != expected {
		t.Errorf("expected:\n%s\ngot:\n%s", expected, got)
	}
}

func TestGenerateTop_InitSls(t *testing.T) {
	states := map[string]string{
		"k3s/init.sls": "install k3s...",
	}
	got := generateTop(states)
	expected := "base:\n  '*':\n    - k3s\n"
	if got != expected {
		t.Errorf("expected:\n%s\ngot:\n%s", expected, got)
	}
}

func TestGenerateTop_MultipleStates_Sorted(t *testing.T) {
	states := map[string]string{
		"nginx.sls":      "...",
		"docker.sls":     "...",
		"k3s/init.sls":   "...",
		"app/deploy.sls": "...",
	}
	got := generateTop(states)
	expected := "base:\n  '*':\n    - app/deploy\n    - docker\n    - k3s\n    - nginx\n"
	if got != expected {
		t.Errorf("expected:\n%s\ngot:\n%s", expected, got)
	}
}

func TestYamlQuote_Plain(t *testing.T) {
	tests := []struct {
		input, expected string
	}{
		{"hello", "hello"},
		{"192.168.1.1", "192.168.1.1"},
		{"some-value", "some-value"},
	}
	for _, tt := range tests {
		if got := yamlQuote(tt.input); got != tt.expected {
			t.Errorf("yamlQuote(%q) = %q, want %q", tt.input, got, tt.expected)
		}
	}
}

func TestYamlQuote_NeedsQuoting(t *testing.T) {
	tests := []string{
		"has: colon",
		"has\nnewline",
		"true",
		"false",
		"null",
		"",
		"curly{brace",
		"pipe|char",
		"star*glob",
		`has"quote`,
	}
	for _, input := range tests {
		got := yamlQuote(input)
		if got == input {
			t.Errorf("yamlQuote(%q) should have quoted but returned as-is", input)
		}
		if !strings.HasPrefix(got, `"`) {
			t.Errorf("yamlQuote(%q) = %q, expected double-quoted string", input, got)
		}
	}
}

func TestBuildSaltCallCmd_NoPillar(t *testing.T) {
	cmd := buildSaltCallCmdWithRoot(nil, false, 0, "/var/lib/salt-tf/test")
	if !strings.Contains(cmd, "salt-call") {
		t.Errorf("expected salt-call in command, got: %s", cmd)
	}
	if strings.Contains(cmd, "--pillar-root") {
		t.Errorf("should not have --pillar-root without pillar data, got: %s", cmd)
	}
	if !strings.HasSuffix(cmd, "state.apply") {
		t.Errorf("should end with state.apply, got: %s", cmd)
	}
}

func TestBuildSaltCallCmd_WithPillar(t *testing.T) {
	cmd := buildSaltCallCmdWithRoot(map[string]string{"key": "val"}, false, 0, "/var/lib/salt-tf/test")
	if !strings.Contains(cmd, "--pillar-root=/var/lib/salt-tf/test/pillar") {
		t.Errorf("expected --pillar-root, got: %s", cmd)
	}
}

func TestBuildSaltCallCmd_TestMode(t *testing.T) {
	cmd := buildSaltCallCmdWithRoot(nil, true, 0, "/var/lib/salt-tf/test")
	if !strings.HasSuffix(cmd, "state.apply test=True") {
		t.Errorf("expected test=True suffix, got: %s", cmd)
	}
}

func TestBuildSaltCallCmd_WithTimeout(t *testing.T) {
	cmd := buildSaltCallCmdWithRoot(nil, false, 300, "/var/lib/salt-tf/test")
	if !strings.HasPrefix(cmd, "timeout 300 ") || !strings.Contains(cmd, "salt-call") {
		t.Errorf("expected timeout prefix, got: %s", cmd)
	}
}

func TestBuildSaltCallCmd_ZeroTimeout(t *testing.T) {
	cmd := buildSaltCallCmdWithRoot(nil, false, 0, "/var/lib/salt-tf/test")
	if strings.Contains(cmd, "timeout") {
		t.Errorf("expected no timeout prefix with 0, got: %s", cmd)
	}
}

func TestTruncate(t *testing.T) {
	short := "hello"
	if got := truncate(short, 100); got != short {
		t.Errorf("short string should not be truncated, got: %q", got)
	}

	long := strings.Repeat("a", 200)
	got := truncate(long, 50)
	if len(got) > 70 { // 50 + "... (truncated)"
		t.Errorf("expected truncation, got length %d", len(got))
	}
	if !strings.Contains(got, "truncated") {
		t.Errorf("expected truncation marker, got: %q", got)
	}
}
