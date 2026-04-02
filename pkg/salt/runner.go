package salt

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/stefanob/terraform-provider-salt/pkg/ssh"
)

// BaseDir is the root directory for all salt-tf resources on remote hosts.
const BaseDir = "/var/lib/salt-tf"

// WorkDir returns the per-resource working directory on the remote host.
func WorkDir(resourceID string) string {
	return fmt.Sprintf("%s/%s", BaseDir, resourceID)
}

// Result holds the parsed output from a salt-call run.
type Result struct {
	Success bool
	InSync  bool
	Changes map[string]StateResult
	Hash    string
	RawJSON string
	Stderr  string // stderr from salt-call (warnings, errors, tracebacks)
}

// StateResult represents a single state result from salt-call output.
type StateResult struct {
	ID      string
	Name    string
	SLS     string
	Result  bool
	Comment string
	Changes map[string]interface{}
}

// Summary returns a human-readable summary of all state results, grouped by
// status (failed first, then changed, then unchanged).
func (r *Result) Summary() string {
	var failed, changed, ok []StateResult
	for _, sr := range r.Changes {
		if !sr.Result {
			failed = append(failed, sr)
		} else if len(sr.Changes) > 0 {
			changed = append(changed, sr)
		} else {
			ok = append(ok, sr)
		}
	}

	var b strings.Builder

	if len(failed) > 0 {
		b.WriteString(fmt.Sprintf("Failed (%d):\n", len(failed)))
		sortStateResults(failed)
		for _, sr := range failed {
			writeStateDetail(&b, sr)
		}
	}

	if len(changed) > 0 {
		b.WriteString(fmt.Sprintf("Changed (%d):\n", len(changed)))
		sortStateResults(changed)
		for _, sr := range changed {
			writeStateDetail(&b, sr)
		}
	}

	if len(ok) > 0 {
		b.WriteString(fmt.Sprintf("Unchanged (%d):\n", len(ok)))
		sortStateResults(ok)
		for _, sr := range ok {
			b.WriteString(fmt.Sprintf("  ✓ %s\n", sr.Name))
		}
	}

	if r.Stderr != "" {
		stderr := CleanStderr(r.Stderr)
		if stderr != "" {
			b.WriteString("\nSalt warnings/errors:\n")
			for _, line := range strings.Split(stderr, "\n") {
				b.WriteString("  " + line + "\n")
			}
		}
	}

	return strings.TrimRight(b.String(), "\n")
}

// FailedStates returns a human-readable summary of only the failed states.
func (r *Result) FailedStates() string {
	var b strings.Builder
	var failed []StateResult
	for _, sr := range r.Changes {
		if !sr.Result {
			failed = append(failed, sr)
		}
	}
	sortStateResults(failed)

	for _, sr := range failed {
		writeStateDetail(&b, sr)
	}

	if r.Stderr != "" {
		stderr := CleanStderr(r.Stderr)
		if stderr != "" {
			b.WriteString("\nSalt stderr:\n")
			for _, line := range strings.Split(stderr, "\n") {
				b.WriteString("  " + line + "\n")
			}
		}
	}

	return strings.TrimRight(b.String(), "\n")
}

func writeStateDetail(b *strings.Builder, sr StateResult) {
	status := "✓"
	if !sr.Result {
		status = "✗"
	}

	sls := ""
	if sr.SLS != "" {
		sls = fmt.Sprintf(" (sls: %s)", sr.SLS)
	}

	b.WriteString(fmt.Sprintf("  %s %s%s\n", status, sr.Name, sls))

	if sr.Comment != "" {
		b.WriteString(fmt.Sprintf("    Comment: %s\n", sr.Comment))
	}

	for k, v := range sr.Changes {
		vs := fmt.Sprintf("%v", v)
		// Truncate long change values for readability
		if len(vs) > 200 {
			vs = vs[:200] + "..."
		}
		b.WriteString(fmt.Sprintf("    %s: %s\n", k, vs))
	}
}

func sortStateResults(srs []StateResult) {
	sort.Slice(srs, func(i, j int) bool {
		return srs[i].ID < srs[j].ID
	})
}

// CleanStderr filters out noise from salt-call stderr (e.g. log file warnings
// that appear on every run and aren't actionable).
func CleanStderr(stderr string) string {
	var lines []string
	for _, line := range strings.Split(strings.TrimSpace(stderr), "\n") {
		// Skip the common "failed to open log file" warning — not actionable
		if strings.Contains(line, "Failed to open log file") {
			continue
		}
		if strings.TrimSpace(line) == "" {
			continue
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}

// UploadStates uploads state files to the remote host under workDir
// and generates a top.sls that includes all uploaded states.
func UploadStates(client *ssh.Client, states map[string]string, workDir string) error {
	// Ensure base dir exists, then clean and recreate this resource's workDir
	_, _ = client.Run(fmt.Sprintf("sudo mkdir -p %s && sudo chmod 777 %s", BaseDir, BaseDir))
	_, _ = client.Run(fmt.Sprintf("rm -rf %s && mkdir -p %s", workDir, workDir))

	for path, content := range states {
		remotePath := fmt.Sprintf("%s/%s", workDir, path)
		if err := client.Upload(remotePath, []byte(content)); err != nil {
			return fmt.Errorf("uploading %s: %w", path, err)
		}
	}

	// Generate top.sls from uploaded state file paths
	topContent := generateTop(states)
	if err := client.Upload(workDir+"/top.sls", []byte(topContent)); err != nil {
		return fmt.Errorf("uploading top.sls: %w", err)
	}

	return nil
}

// generateTop builds a top.sls that maps all state files to all minions.
func generateTop(states map[string]string) string {
	var names []string
	for path := range states {
		name := strings.TrimSuffix(path, ".sls")
		name = strings.TrimSuffix(name, "/init")
		names = append(names, name)
	}
	sort.Strings(names)

	var b strings.Builder
	b.WriteString("base:\n  '*':\n")
	for _, name := range names {
		b.WriteString(fmt.Sprintf("    - %s\n", name))
	}
	return b.String()
}

// UploadPillar writes pillar data as a YAML file on the remote host.
func UploadPillar(client *ssh.Client, pillar map[string]string, workDir string) error {
	if len(pillar) == 0 {
		return nil
	}

	pillarDir := workDir + "/pillar"
	_, _ = client.Run(fmt.Sprintf("mkdir -p %s", pillarDir))

	// Build a simple YAML from the flat map
	var lines []string
	keys := sortedKeys(pillar)
	for _, k := range keys {
		lines = append(lines, fmt.Sprintf("%s: %s", k, yamlQuote(pillar[k])))
	}
	content := strings.Join(lines, "\n") + "\n"

	if err := client.Upload(pillarDir+"/custom.sls", []byte(content)); err != nil {
		return fmt.Errorf("uploading pillar: %w", err)
	}

	// Write pillar top.sls
	topContent := "base:\n  '*':\n    - custom\n"
	if err := client.Upload(pillarDir+"/top.sls", []byte(topContent)); err != nil {
		return fmt.Errorf("uploading pillar top.sls: %w", err)
	}

	return nil
}

// Apply runs salt-call --local state.apply and returns the parsed result.
// workDir is the directory containing state files and pillar data.
// timeoutSecs is the maximum execution time in seconds; 0 means no timeout.
func Apply(client *ssh.Client, pillar map[string]string, workDir string, timeoutSecs int) (*Result, error) {
	cmd := buildSaltCallCmdWithRoot(pillar, false, timeoutSecs, workDir)
	r, err := client.RunCapture(cmd)
	if err != nil {
		return nil, fmt.Errorf("salt-call failed (SSH error): %w", err)
	}

	if r.Stdout == "" && r.ExitCode != 0 {
		return nil, fmt.Errorf(
			"salt-call exited with code %d and no JSON output.\n\nstderr:\n%s",
			r.ExitCode, r.Stderr,
		)
	}

	result, parseErr := parseResult(r.Stdout)
	if parseErr != nil {
		return nil, fmt.Errorf("%w\n\nstderr:\n%s", parseErr, r.Stderr)
	}

	result.Stderr = r.Stderr
	return result, nil
}

// Test runs salt-call --local state.apply test=True (dry run) to detect drift.
// workDir is the directory containing state files and pillar data.
// timeoutSecs is the maximum execution time in seconds; 0 means no timeout.
func Test(client *ssh.Client, pillar map[string]string, workDir string, timeoutSecs int) (*Result, error) {
	cmd := buildSaltCallCmdWithRoot(pillar, true, timeoutSecs, workDir)
	r, err := client.RunCapture(cmd)
	if err != nil {
		return nil, fmt.Errorf("salt-call test failed (SSH error): %w", err)
	}

	if r.Stdout == "" && r.ExitCode != 0 {
		return nil, fmt.Errorf(
			"salt-call test exited with code %d and no JSON output.\n\nstderr:\n%s",
			r.ExitCode, r.Stderr,
		)
	}

	result, parseErr := parseResult(r.Stdout)
	if parseErr != nil {
		return nil, fmt.Errorf("%w\n\nstderr:\n%s", parseErr, r.Stderr)
	}

	result.Stderr = r.Stderr
	return result, nil
}

func buildSaltCallCmdWithRoot(pillar map[string]string, testMode bool, timeoutSecs int, workDir string) string {
	var prefix string
	if timeoutSecs > 0 {
		prefix = fmt.Sprintf("timeout %d ", timeoutSecs)
	}

	cmd := fmt.Sprintf(
		"%ssudo salt-call --local --file-root=%s --out=json --out-file=/dev/stdout --retcode-passthrough",
		prefix, workDir,
	)

	if len(pillar) > 0 {
		cmd += fmt.Sprintf(" --pillar-root=%s/pillar", workDir)
	}

	cmd += " state.apply"

	if testMode {
		cmd += " test=True"
	}

	return cmd
}

func parseResult(rawJSON string) (*Result, error) {
	// salt-call --out=json wraps output under "local" key.
	// On success: {"local": {"state_id": {...}, ...}}
	// On error:   {"local": ["error message"]}
	// We need to handle both.

	var rawTop map[string]json.RawMessage
	if err := json.Unmarshal([]byte(rawJSON), &rawTop); err != nil {
		return nil, fmt.Errorf("failed to parse salt-call output as JSON: %w\n\nRaw output:\n%s", err, truncate(rawJSON, 1000))
	}

	localRaw, ok := rawTop["local"]
	if !ok {
		return nil, fmt.Errorf("unexpected salt-call output: missing 'local' key\n\nRaw output:\n%s", truncate(rawJSON, 1000))
	}

	// Check if "local" is an error array (Salt returns errors this way)
	var errorMessages []string
	if err := json.Unmarshal(localRaw, &errorMessages); err == nil {
		return nil, fmt.Errorf("Salt returned an error:\n%s", strings.Join(errorMessages, "\n"))
	}

	// Parse the normal state result map
	var stateMap map[string]json.RawMessage
	if err := json.Unmarshal(localRaw, &stateMap); err != nil {
		return nil, fmt.Errorf("failed to parse salt state results: %w\n\nRaw output:\n%s", err, truncate(rawJSON, 1000))
	}

	result := &Result{
		Success: true,
		InSync:  true,
		Changes: make(map[string]StateResult),
		RawJSON: rawJSON,
		Hash:    fmt.Sprintf("%x", sha256.Sum256([]byte(rawJSON))),
	}

	for id, rawState := range stateMap {
		var state struct {
			Name    string                 `json:"name"`
			Result  bool                   `json:"result"`
			Comment string                 `json:"comment"`
			Changes map[string]interface{} `json:"changes"`
			SLS     string                 `json:"__sls__"`
		}
		if err := json.Unmarshal(rawState, &state); err != nil {
			// Include the raw state in the error so users can debug
			return nil, fmt.Errorf("failed to parse state %q: %w\n\nRaw state:\n%s", id, err, string(rawState))
		}

		sr := StateResult{
			ID:      id,
			Name:    state.Name,
			SLS:     state.SLS,
			Result:  state.Result,
			Comment: state.Comment,
			Changes: state.Changes,
		}

		if !state.Result {
			result.Success = false
		}
		if len(state.Changes) > 0 {
			result.InSync = false
		}

		result.Changes[id] = sr
	}

	return result, nil
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "\n... (truncated)"
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func yamlQuote(s string) string {
	// Quote strings that could be misinterpreted by YAML
	if strings.ContainsAny(s, ":{}[]&*?|>!%@`'\"\n") || s == "true" || s == "false" || s == "null" || s == "" {
		return fmt.Sprintf("%q", s)
	}
	return s
}
