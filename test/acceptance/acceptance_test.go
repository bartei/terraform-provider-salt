package acceptance

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/bartei/terraform-provider-salt/pkg/salt"
	"github.com/bartei/terraform-provider-salt/pkg/ssh"
)

// These tests require a running QEMU VM. Start one with:
//
//	make vm-start
//
// Then run with:
//
//	make testacc-vm
//
// Environment variables:
//
//	ACC_SSH_HOST     — VM host (default: localhost)
//	ACC_SSH_PORT     — VM SSH port (default: 2222)
//	ACC_SSH_USER     — VM SSH user (default: test)
//	ACC_SSH_KEY_FILE — path to private key (default: test/acceptance/.vm/id_ed25519)

func getEnvOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// projectRoot walks up from the test file's directory to find the go.mod.
func projectRoot() string {
	// go test sets the working directory to the package directory
	dir, _ := os.Getwd()
	for {
		if _, err := os.Stat(dir + "/go.mod"); err == nil {
			return dir
		}
		parent := dir[:max(0, len(dir)-len(dir[strings.LastIndex(dir, "/")+1:])-1)]
		if parent == dir || parent == "" {
			return "."
		}
		dir = parent
	}
}

func sshPort() int {
	p, err := strconv.Atoi(getEnvOr("ACC_SSH_PORT", "2222"))
	if err != nil {
		return 2222
	}
	return p
}

func connectVM(t *testing.T) *ssh.Client {
	t.Helper()

	host := getEnvOr("ACC_SSH_HOST", "localhost")
	port := sshPort()
	user := getEnvOr("ACC_SSH_USER", "test")
	defaultKeyFile := filepath.Join(projectRoot(), "test", "acceptance", ".vm", "id_ed25519")
	keyFile := getEnvOr("ACC_SSH_KEY_FILE", defaultKeyFile)

	keyData, err := os.ReadFile(keyFile)
	if err != nil {
		t.Fatalf("Cannot read SSH key %s: %v", keyFile, err)
	}

	client, err := ssh.NewClient(host, port, user, string(keyData))
	if err != nil {
		t.Fatalf("Cannot connect to VM at %s:%d: %v", host, port, err)
	}
	return client
}

func TestSSHConnectivity(t *testing.T) {
	if os.Getenv("TF_ACC") == "" {
		t.Skip("Set TF_ACC=1 to run acceptance tests")
	}

	client := connectVM(t)
	defer func() { _ = client.Close() }()

	out, err := client.Run("echo hello")
	if err != nil {
		t.Fatalf("Failed to run command: %v", err)
	}
	if out != "hello\n" {
		t.Fatalf("Unexpected output: %q", out)
	}
}

func TestSaltBootstrapAndApply(t *testing.T) {
	if os.Getenv("TF_ACC") == "" {
		t.Skip("Set TF_ACC=1 to run acceptance tests")
	}

	client := connectVM(t)
	defer func() { _ = client.Close() }()

	// Step 1: Bootstrap Salt
	t.Log("Bootstrapping Salt...")
	err := salt.EnsureVersion(client, "3007")
	if err != nil {
		t.Fatalf("Salt bootstrap failed: %v", err)
	}

	// Verify salt-call works
	out, err := client.Run("salt-call --version")
	if err != nil {
		t.Fatalf("salt-call not available after bootstrap: %v", err)
	}
	t.Logf("Salt version: %s", out)

	// Step 2: Upload and apply a simple state
	states := map[string]string{
		"test.sls": `
create_test_file:
  file.managed:
    - name: /tmp/salt-acc-test
    - contents: "hello from salt"
`,
	}

	t.Log("Uploading states...")
	if err := salt.UploadStates(client, states, salt.WorkDir("acc-test")); err != nil {
		t.Fatalf("Upload states failed: %v", err)
	}

	t.Log("Applying states...")
	result, err := salt.Apply(client, nil, salt.WorkDir("acc-test"), 0)
	if err != nil {
		t.Fatalf("Salt apply failed: %v", err)
	}

	if !result.Success {
		t.Fatalf("Salt apply reported failure:\n%s", result.FailedStates())
	}
	t.Logf("Apply result: success=%v, in_sync=%v, changes=%d",
		result.Success, result.InSync, len(result.Changes))

	// Verify the file was created
	out, err = client.Run("cat /tmp/salt-acc-test")
	if err != nil {
		t.Fatalf("File not created: %v", err)
	}
	if got := strings.TrimSpace(out); got != "hello from salt" {
		t.Fatalf("Unexpected file content: %q", got)
	}

	// Step 3: Run again — should be in sync (idempotent)
	t.Log("Applying again (idempotent check)...")
	result2, err := salt.Apply(client, nil, salt.WorkDir("acc-test"), 0)
	if err != nil {
		t.Fatalf("Second apply failed: %v", err)
	}
	if !result2.InSync {
		t.Fatalf("Expected in_sync=true on second apply, got false")
	}

	// Cleanup
	_, _ = client.Run("rm -f /tmp/salt-acc-test")
}

func TestDriftDetection(t *testing.T) {
	if os.Getenv("TF_ACC") == "" {
		t.Skip("Set TF_ACC=1 to run acceptance tests")
	}

	client := connectVM(t)
	defer func() { _ = client.Close() }()

	// Ensure Salt is installed (may already be from previous test)
	if err := salt.EnsureVersion(client, "3007"); err != nil {
		t.Fatalf("Salt bootstrap failed: %v", err)
	}

	states := map[string]string{
		"drift.sls": `
drift_test_file:
  file.managed:
    - name: /tmp/salt-drift-test
    - contents: "managed content"
`,
	}

	// Apply initial state
	if err := salt.UploadStates(client, states, salt.WorkDir("acc-test")); err != nil {
		t.Fatalf("Upload failed: %v", err)
	}
	result, err := salt.Apply(client, nil, salt.WorkDir("acc-test"), 0)
	if err != nil {
		t.Fatalf("Apply failed: %v", err)
	}
	if !result.Success {
		t.Fatalf("Apply failed:\n%s", result.FailedStates())
	}

	// Verify in-sync via test mode
	t.Log("Checking drift (should be in sync)...")
	testResult, err := salt.Test(client, nil, salt.WorkDir("acc-test"), 0)
	if err != nil {
		t.Fatalf("Test mode failed: %v", err)
	}
	if !testResult.InSync {
		t.Fatalf("Expected in_sync=true immediately after apply")
	}

	// Introduce drift by modifying the file
	t.Log("Introducing drift...")
	_, err = client.Run("sudo sh -c \"echo tampered > /tmp/salt-drift-test\"")
	if err != nil {
		t.Fatalf("Failed to introduce drift: %v", err)
	}

	// Re-upload states (needed because Test reads from stateDir)
	if err := salt.UploadStates(client, states, salt.WorkDir("acc-test")); err != nil {
		t.Fatalf("Re-upload failed: %v", err)
	}

	// Drift detection should catch the change
	t.Log("Checking drift (should detect change)...")
	driftResult, err := salt.Test(client, nil, salt.WorkDir("acc-test"), 0)
	if err != nil {
		t.Fatalf("Drift test failed: %v", err)
	}
	if driftResult.InSync {
		t.Fatalf("Expected in_sync=false after tampering, got true")
	}

	// Cleanup
	_, _ = client.Run("rm -f /tmp/salt-drift-test")
}

func TestPillarData(t *testing.T) {
	if os.Getenv("TF_ACC") == "" {
		t.Skip("Set TF_ACC=1 to run acceptance tests")
	}

	client := connectVM(t)
	defer func() { _ = client.Close() }()

	if err := salt.EnsureVersion(client, "3007"); err != nil {
		t.Fatalf("Salt bootstrap failed: %v", err)
	}

	states := map[string]string{
		"pillar_test.sls": `
pillar_file:
  file.managed:
    - name: /tmp/salt-pillar-test
    - contents: {{ pillar['greeting'] }}
`,
	}

	pillar := map[string]string{
		"greeting": "hello-from-pillar",
	}

	if err := salt.UploadStates(client, states, salt.WorkDir("acc-test")); err != nil {
		t.Fatalf("Upload states failed: %v", err)
	}
	if err := salt.UploadPillar(client, pillar, salt.WorkDir("acc-test")); err != nil {
		t.Fatalf("Upload pillar failed: %v", err)
	}

	result, err := salt.Apply(client, pillar, salt.WorkDir("acc-test"), 0)
	if err != nil {
		t.Fatalf("Apply failed: %v", err)
	}
	if !result.Success {
		t.Fatalf("Apply failed:\n%s", result.FailedStates())
	}

	out, err := client.Run("cat /tmp/salt-pillar-test")
	if err != nil {
		t.Fatalf("File not created: %v", err)
	}
	expected := "hello-from-pillar"
	if got := strings.TrimSpace(out); got != expected {
		t.Fatalf("Expected %q, got %q", expected, got)
	}

	// Cleanup
	_, _ = client.Run(fmt.Sprintf("rm -rf %s /tmp/salt-pillar-test", "/var/lib/salt-tf"))
}
