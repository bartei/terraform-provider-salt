#!/usr/bin/env bash
#
# e2e.sh — end-to-end test: build the provider, run terraform apply/plan/destroy
#
# Prerequisites:
#   - QEMU VM running (make vm-start)
#   - terraform installed
#   - go installed
#
# Usage:
#   ./test/acceptance/e2e.sh
#
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "${SCRIPT_DIR}/../.." && pwd)"
VM_DIR="${SCRIPT_DIR}/.vm"
WORK_DIR="${SCRIPT_DIR}/.e2e"
SSH_KEY="${VM_DIR}/id_ed25519"

RED='\033[0;31m'
GREEN='\033[0;32m'
BOLD='\033[1m'
RESET='\033[0m'

pass() { echo -e "${GREEN}✓ $*${RESET}"; }
fail() { echo -e "${RED}✗ $*${RESET}"; exit 1; }
step() { echo -e "\n${BOLD}── $* ──${RESET}"; }

cleanup() {
    if [[ -d "${WORK_DIR}" ]]; then
        cd "${WORK_DIR}"
        TF_CLI_CONFIG_FILE="${WORK_DIR}/dev.tfrc" terraform destroy -auto-approve \
            -var="ssh_private_key_file=${SSH_KEY}" >/dev/null 2>&1 || true
        rm -rf "${WORK_DIR}"
    fi
}
trap cleanup EXIT

# ── Preflight checks ──
[[ -f "${SSH_KEY}" ]] || fail "SSH key not found at ${SSH_KEY}. Run 'make vm-start' first."
command -v terraform >/dev/null || fail "terraform not found in PATH"
command -v go >/dev/null || fail "go not found in PATH"

# ── Step 1: Build the provider ──
step "Building provider"
cd "${PROJECT_ROOT}"
go build -o "${PROJECT_ROOT}/terraform-provider-salt" .
pass "Provider binary built"

# ── Step 2: Set up working directory ──
step "Setting up Terraform workspace"
rm -rf "${WORK_DIR}"
mkdir -p "${WORK_DIR}"

# Copy the example config
cp "${PROJECT_ROOT}/examples/basic/main.tf" "${WORK_DIR}/main.tf"

# Create dev override so Terraform uses our local binary
cat > "${WORK_DIR}/dev.tfrc" <<EOF
provider_installation {
  dev_overrides {
    "registry.terraform.io/bartei/salt" = "${PROJECT_ROOT}"
  }
  direct {}
}
EOF

export TF_CLI_CONFIG_FILE="${WORK_DIR}/dev.tfrc"
cd "${WORK_DIR}"
pass "Workspace ready at ${WORK_DIR}"

# ── Step 3: Terraform apply ──
step "terraform apply"
APPLY_START=$(date +%s)
terraform apply -auto-approve \
    -var="ssh_private_key_file=${SSH_KEY}" \
    -var='greeting=Hello from Terraform + Salt!'
APPLY_DURATION=$(( $(date +%s) - APPLY_START ))
pass "Apply succeeded (${APPLY_DURATION}s)"

# ── Step 3b: Verify masterless invariants ──
# Cold bootstrap must not hang on salt-minion's master DNS-retry loop. We
# install masterless: the minion package is on disk for salt-call, but the
# service itself must be killed/disabled/masked.
step "Verifying masterless invariants"

# systemctl is-{enabled,active} return non-zero when the unit is in a
# state the test expects (masked / failed) — wrap each in `|| true` so
# `set -e` doesn't kill the script.
MINION_STATE=$(ssh -p 2222 -i "${SSH_KEY}" \
    -o StrictHostKeyChecking=no \
    -o UserKnownHostsFile=/dev/null \
    test@localhost "systemctl is-enabled salt-minion 2>&1 || true; systemctl is-active salt-minion 2>&1 || true" 2>/dev/null)

if echo "${MINION_STATE}" | head -1 | grep -q '^masked$'; then
    pass "salt-minion is masked"
else
    fail "salt-minion should be masked, got: $(echo "${MINION_STATE}" | head -1)"
fi

if echo "${MINION_STATE}" | tail -1 | grep -qE '^(inactive|failed)$'; then
    pass "salt-minion is not running"
else
    fail "salt-minion should be inactive/failed, got: $(echo "${MINION_STATE}" | tail -1)"
fi

# If the bootstrap path took longer than ~60s we very likely re-introduced
# the DNS-retry hang. A clean cold install completes in well under 30s.
if (( APPLY_DURATION > 60 )); then
    fail "Cold apply took ${APPLY_DURATION}s — suspect salt-minion DNS-retry hang regression"
else
    pass "Cold apply finished within 60s (${APPLY_DURATION}s)"
fi

# Count salt-minion "Retrying in 30 seconds" lines. The package postinst
# may briefly start the minion before we kill it, so one line is OK; two
# or more means we waited at least 30s for graceful shutdown.
RETRY_COUNT=$(ssh -p 2222 -i "${SSH_KEY}" \
    -o StrictHostKeyChecking=no \
    -o UserKnownHostsFile=/dev/null \
    test@localhost "sudo journalctl -u salt-minion --no-pager 2>/dev/null | grep -c 'Retrying in 30 seconds' || true" 2>/dev/null | tr -d '[:space:]')

if [[ -z "${RETRY_COUNT}" ]] || (( RETRY_COUNT <= 1 )); then
    pass "No DNS-retry hang during bootstrap (${RETRY_COUNT:-0} retry lines)"
else
    fail "Bootstrap hung in DNS-retry loop (${RETRY_COUNT} 'Retrying in 30 seconds' lines)"
fi

# ── Step 4: Verify the file was created on the VM ──
step "Verifying managed file on VM"
ACTUAL=$(ssh -p 2222 -i "${SSH_KEY}" \
    -o StrictHostKeyChecking=no \
    -o UserKnownHostsFile=/dev/null \
    test@localhost "cat /tmp/terraform-salt-example" 2>/dev/null)

EXPECTED="Hello from Terraform + Salt!"
if [[ "$(echo "${ACTUAL}" | tr -d '\n')" == "${EXPECTED}" ]]; then
    pass "File content matches: ${EXPECTED}"
else
    fail "File content mismatch.\n  Expected: ${EXPECTED}\n  Actual:   ${ACTUAL}"
fi

# ── Step 5: Terraform plan (should show no changes = no drift) ──
step "terraform plan (drift check)"
PLAN_OUTPUT=$(terraform plan -detailed-exitcode \
    -var="ssh_private_key_file=${SSH_KEY}" \
    -var='greeting=Hello from Terraform + Salt!' 2>&1) && PLAN_EXIT=0 || PLAN_EXIT=$?

if [[ ${PLAN_EXIT} -eq 0 ]]; then
    pass "No changes detected (infrastructure is in sync)"
elif [[ ${PLAN_EXIT} -eq 2 ]]; then
    # Exit code 2 means changes detected — could be drift or state diff
    echo "${PLAN_OUTPUT}"
    fail "Unexpected changes detected after apply"
else
    echo "${PLAN_OUTPUT}"
    fail "Plan failed with exit code ${PLAN_EXIT}"
fi

# ── Step 6: Introduce drift, verify plan detects it ──
step "Introducing drift (tampering with managed file)"
ssh -p 2222 -i "${SSH_KEY}" \
    -o StrictHostKeyChecking=no \
    -o UserKnownHostsFile=/dev/null \
    test@localhost "sudo sh -c 'echo tampered > /tmp/terraform-salt-example'" 2>/dev/null
pass "File tampered"

step "terraform plan (should detect drift)"
PLAN_OUTPUT=$(terraform plan -detailed-exitcode \
    -var="ssh_private_key_file=${SSH_KEY}" \
    -var='greeting=Hello from Terraform + Salt!' 2>&1) && PLAN_EXIT=0 || PLAN_EXIT=$?

if [[ ${PLAN_EXIT} -eq 2 ]]; then
    pass "Drift correctly detected — plan shows changes"
elif [[ ${PLAN_EXIT} -eq 0 ]]; then
    fail "Plan shows no changes, but drift was introduced"
else
    echo "${PLAN_OUTPUT}"
    fail "Plan failed with exit code ${PLAN_EXIT}"
fi

# ── Step 7: Re-apply to fix drift ──
step "terraform apply (fix drift)"
terraform apply -auto-approve \
    -var="ssh_private_key_file=${SSH_KEY}" \
    -var='greeting=Hello from Terraform + Salt!'
pass "Drift repaired"

# Verify file is correct again
ACTUAL=$(ssh -p 2222 -i "${SSH_KEY}" \
    -o StrictHostKeyChecking=no \
    -o UserKnownHostsFile=/dev/null \
    test@localhost "cat /tmp/terraform-salt-example" 2>/dev/null)

if [[ "$(echo "${ACTUAL}" | tr -d '\n')" == "${EXPECTED}" ]]; then
    pass "File content restored: ${EXPECTED}"
else
    fail "File content not restored after re-apply"
fi

# ── Step 8: Terraform destroy ──
step "terraform destroy"
terraform destroy -auto-approve \
    -var="ssh_private_key_file=${SSH_KEY}"
pass "Destroy succeeded"

# Verify cleanup — the specific resource's workDir (76ac2c6a80d0f027) should be gone
if ssh -p 2222 -i "${SSH_KEY}" \
    -o StrictHostKeyChecking=no \
    -o UserKnownHostsFile=/dev/null \
    test@localhost "test -d /var/lib/salt-tf/76ac2c6a80d0f027" 2>/dev/null; then
    fail "Resource working directory /var/lib/salt-tf/76ac2c6a80d0f027 still exists after destroy"
else
    pass "Remote cleanup verified (resource workDir removed)"
fi

# ── Step 9: Test failure output — apply a state that will fail ──
step "terraform apply (expected failure — bad state)"

# Write a tf config with a Salt state that will fail
cat > "${WORK_DIR}/main.tf" <<'TFEOF'
terraform {
  required_providers {
    salt = {
      source = "registry.terraform.io/bartei/salt"
    }
  }
}

provider "salt" {
  salt_version = "3007"
}

variable "ssh_private_key_file" {
  type = string
}

resource "salt_state" "failing" {
  host        = "localhost"
  port        = 2222
  user        = "test"
  private_key = file(var.ssh_private_key_file)

  states = {
    "bad.sls" = <<-SLS
      # This state will fail: writing to a root-owned directory without permission
      write_to_restricted:
        file.managed:
          - name: /etc/this-should-fail-permission-denied
          - contents: "should not work"
          - user: root
          - group: root
          - mode: '0644'

      # This state references an undefined Jinja variable
      bad_jinja:
        file.managed:
          - name: /tmp/jinja-fail
          - contents: {{ pillar['nonexistent_key'] }}
    SLS
  }
}
TFEOF

# This should fail — capture the output
FAIL_OUTPUT=$(terraform apply -auto-approve \
    -var="ssh_private_key_file=${SSH_KEY}" 2>&1) && FAIL_EXIT=0 || FAIL_EXIT=$?

if [[ ${FAIL_EXIT} -eq 0 ]]; then
    fail "Expected apply to fail, but it succeeded"
fi

# Verify error output contains useful debugging information
echo ""
echo "── Error output from failed apply ──"
echo "${FAIL_OUTPUT}" | grep -A 100 "Error:" | head -40
echo "── End error output ──"
echo ""

# Check that the error message contains actionable details
CHECKS_PASSED=0
CHECKS_TOTAL=0

check_output() {
    local description="$1"
    local pattern="$2"
    (( CHECKS_TOTAL++ )) || true
    if echo "${FAIL_OUTPUT}" | grep -qi "${pattern}"; then
        pass "Error output contains: ${description}"
        (( CHECKS_PASSED++ )) || true
    else
        echo -e "${RED}✗ Error output missing: ${description} (pattern: ${pattern})${RESET}"
    fi
}

check_output "host identifier" "localhost"
check_output "error category" "Salt"
check_output "state details or stderr" "failed\|error\|denied\|Jinja\|undefined"

if [[ ${CHECKS_PASSED} -lt ${CHECKS_TOTAL} ]]; then
    fail "Some error output checks failed (${CHECKS_PASSED}/${CHECKS_TOTAL})"
fi
pass "Error output contains actionable debugging information (${CHECKS_PASSED}/${CHECKS_TOTAL} checks)"

# Clean up the failed state (no terraform state to destroy, just remove the work dir)
rm -f "${WORK_DIR}/terraform.tfstate" "${WORK_DIR}/terraform.tfstate.backup"

# ── Step 10: Test salt_grains data source ──
step "terraform apply (salt_grains data source)"

cat > "${WORK_DIR}/main.tf" <<'TFEOF'
terraform {
  required_providers {
    salt = { source = "registry.terraform.io/bartei/salt" }
  }
}

provider "salt" {}

variable "ssh_private_key_file" { type = string }

data "salt_grains" "vm" {
  host        = "localhost"
  port        = 2222
  user        = "test"
  private_key = file(var.ssh_private_key_file)
}

output "os" {
  value = data.salt_grains.vm.values["os"]
}

output "kernel" {
  value = data.salt_grains.vm.values["kernel"]
}

output "grain_count" {
  value = length(data.salt_grains.vm.values)
}
TFEOF

terraform apply -auto-approve -var="ssh_private_key_file=${SSH_KEY}"

OS_VAL=$(terraform output -raw os)
KERNEL_VAL=$(terraform output -raw kernel)
GRAIN_COUNT=$(terraform output -raw grain_count)

if [[ "${OS_VAL}" == "Debian" ]]; then
    pass "Grains: os = ${OS_VAL}"
else
    fail "Expected os=Debian, got: ${OS_VAL}"
fi

if [[ "${KERNEL_VAL}" == "Linux" ]]; then
    pass "Grains: kernel = ${KERNEL_VAL}"
else
    fail "Expected kernel=Linux, got: ${KERNEL_VAL}"
fi

if [[ "${GRAIN_COUNT}" -gt 20 ]]; then
    pass "Grains: ${GRAIN_COUNT} grains returned"
else
    fail "Expected >20 grains, got: ${GRAIN_COUNT}"
fi

rm -f "${WORK_DIR}/terraform.tfstate" "${WORK_DIR}/terraform.tfstate.backup"

# ── Step 11: Test salt_pillar data source ──
step "terraform apply (salt_pillar data source)"

# First set up some pillar data on the VM
ssh -p 2222 -i "${SSH_KEY}" \
    -o StrictHostKeyChecking=no \
    -o UserKnownHostsFile=/dev/null \
    test@localhost "sudo mkdir -p /srv/pillar && sudo tee /srv/pillar/top.sls > /dev/null <<'EOF'
base:
  '*':
    - test_pillar
EOF
sudo tee /srv/pillar/test_pillar.sls > /dev/null <<'EOF'
test_key: test_value
environment: staging
EOF" 2>/dev/null
pass "Pillar data configured on VM"

cat > "${WORK_DIR}/main.tf" <<'TFEOF'
terraform {
  required_providers {
    salt = { source = "registry.terraform.io/bartei/salt" }
  }
}

provider "salt" {}

variable "ssh_private_key_file" { type = string }

data "salt_pillar" "vm" {
  host        = "localhost"
  port        = 2222
  user        = "test"
  private_key = file(var.ssh_private_key_file)
}

output "test_key" {
  value = data.salt_pillar.vm.values["test_key"]
}

output "environment" {
  value = data.salt_pillar.vm.values["environment"]
}
TFEOF

terraform apply -auto-approve -var="ssh_private_key_file=${SSH_KEY}"

PILLAR_TEST_KEY=$(terraform output -raw test_key)
PILLAR_ENV=$(terraform output -raw environment)

if [[ "${PILLAR_TEST_KEY}" == "test_value" ]]; then
    pass "Pillar: test_key = ${PILLAR_TEST_KEY}"
else
    fail "Expected test_key=test_value, got: ${PILLAR_TEST_KEY}"
fi

if [[ "${PILLAR_ENV}" == "staging" ]]; then
    pass "Pillar: environment = ${PILLAR_ENV}"
else
    fail "Expected environment=staging, got: ${PILLAR_ENV}"
fi

rm -f "${WORK_DIR}/terraform.tfstate" "${WORK_DIR}/terraform.tfstate.backup"

# ── Step 12: Test salt_formula resource ──
step "terraform apply (salt_formula resource)"

# Create a minimal Salt formula repo on the VM (local git repo)
ssh -p 2222 -i "${SSH_KEY}" \
    -o StrictHostKeyChecking=no \
    -o UserKnownHostsFile=/dev/null \
    test@localhost "
sudo apt-get install -y git >/dev/null 2>&1
rm -rf /tmp/test-formula
mkdir -p /tmp/test-formula/testformula
cd /tmp/test-formula
git init
git config user.email 'test@test'
git config user.name 'test'
cat > testformula/init.sls <<'SLS'
formula_test_file:
  file.managed:
    - name: /tmp/formula-test-output
    - contents: formula-applied-successfully
SLS
git add -A
git commit -m 'initial'
" 2>/dev/null
pass "Test formula git repo created on VM"

cat > "${WORK_DIR}/main.tf" <<'TFEOF'
terraform {
  required_providers {
    salt = { source = "registry.terraform.io/bartei/salt" }
  }
}

provider "salt" {
  salt_version = "3007"
}

variable "ssh_private_key_file" { type = string }

resource "salt_formula" "test" {
  host        = "localhost"
  port        = 2222
  user        = "test"
  private_key = file(var.ssh_private_key_file)
  repo_url    = "/tmp/test-formula"
}

output "formula_hash" {
  value = salt_formula.test.applied_hash
}
TFEOF

terraform apply -auto-approve -var="ssh_private_key_file=${SSH_KEY}"
pass "Formula apply succeeded"

# Verify the formula created the expected file
FORMULA_OUTPUT=$(ssh -p 2222 -i "${SSH_KEY}" \
    -o StrictHostKeyChecking=no \
    -o UserKnownHostsFile=/dev/null \
    test@localhost "cat /tmp/formula-test-output" 2>/dev/null)

if [[ "$(echo "${FORMULA_OUTPUT}" | tr -d '\n')" == "formula-applied-successfully" ]]; then
    pass "Formula created file with correct content"
else
    fail "Expected 'formula-applied-successfully', got: ${FORMULA_OUTPUT}"
fi

# Destroy and verify cleanup
terraform destroy -auto-approve -var="ssh_private_key_file=${SSH_KEY}"
pass "Formula destroy succeeded"

if ssh -p 2222 -i "${SSH_KEY}" \
    -o StrictHostKeyChecking=no \
    -o UserKnownHostsFile=/dev/null \
    test@localhost "test -d /var/lib/salt-tf-formula" 2>/dev/null; then
    fail "/var/lib/salt-tf-formula still exists after destroy"
else
    pass "Formula remote cleanup verified"
fi

# Clean up test formula and files
ssh -p 2222 -i "${SSH_KEY}" \
    -o StrictHostKeyChecking=no \
    -o UserKnownHostsFile=/dev/null \
    test@localhost "rm -rf /tmp/test-formula /tmp/formula-test-output /srv/pillar" 2>/dev/null || true

rm -f "${WORK_DIR}/terraform.tfstate" "${WORK_DIR}/terraform.tfstate.backup"

# ── Step 13: Test terraform import ──
step "terraform import (adopt existing resource without apply)"

# First, apply a state to set up a known baseline
cat > "${WORK_DIR}/main.tf" <<'TFEOF'
terraform {
  required_providers {
    salt = { source = "registry.terraform.io/bartei/salt" }
  }
}

provider "salt" {
  salt_version = "3007"
}

variable "ssh_private_key_file" { type = string }

resource "salt_state" "imported" {
  host        = "localhost"
  port        = 2222
  user        = "test"
  private_key = file(var.ssh_private_key_file)

  states = {
    "import_test.sls" = <<-SLS
      import_test_file:
        file.managed:
          - name: /tmp/import-test
          - contents: "managed by terraform"
    SLS
  }
}

output "imported_id" {
  value = salt_state.imported.id
}

output "imported_hash" {
  value = salt_state.imported.applied_hash
}
TFEOF

terraform apply -auto-approve -var="ssh_private_key_file=${SSH_KEY}"
RESOURCE_ID=$(terraform output -raw imported_id)
pass "Baseline applied (id=${RESOURCE_ID})"

# Verify the file exists
ssh -p 2222 -i "${SSH_KEY}" \
    -o StrictHostKeyChecking=no \
    -o UserKnownHostsFile=/dev/null \
    test@localhost "cat /tmp/import-test" 2>/dev/null | grep -q "managed by terraform"
pass "Managed file verified on host"

# Now blow away the state (simulating "I have a host managed outside Terraform")
rm -f "${WORK_DIR}/terraform.tfstate" "${WORK_DIR}/terraform.tfstate.backup"
pass "State file deleted (simulating out-of-band management)"

# Import the resource using the known ID
terraform import -var="ssh_private_key_file=${SSH_KEY}" salt_state.imported "${RESOURCE_ID}"
pass "terraform import succeeded"

# Plan should show an update (to re-apply and establish the hash)
PLAN_OUTPUT=$(terraform plan -detailed-exitcode \
    -var="ssh_private_key_file=${SSH_KEY}" 2>&1) && PLAN_EXIT=0 || PLAN_EXIT=$?

if [[ ${PLAN_EXIT} -eq 2 ]]; then
    pass "Plan correctly shows update needed after import"
elif [[ ${PLAN_EXIT} -eq 0 ]]; then
    fail "Expected plan to show changes after import, but got no changes"
else
    echo "${PLAN_OUTPUT}"
    fail "Plan failed with exit code ${PLAN_EXIT}"
fi

# Apply to converge
terraform apply -auto-approve -var="ssh_private_key_file=${SSH_KEY}"
pass "Post-import apply succeeded"

# Now plan should be clean
PLAN_OUTPUT=$(terraform plan -detailed-exitcode \
    -var="ssh_private_key_file=${SSH_KEY}" 2>&1) && PLAN_EXIT=0 || PLAN_EXIT=$?

if [[ ${PLAN_EXIT} -eq 0 ]]; then
    pass "Post-import plan shows no changes (converged)"
elif [[ ${PLAN_EXIT} -eq 2 ]]; then
    echo "${PLAN_OUTPUT}"
    fail "Expected no changes after post-import apply, but plan shows changes"
else
    echo "${PLAN_OUTPUT}"
    fail "Plan failed with exit code ${PLAN_EXIT}"
fi

# Verify the file is still correct
ACTUAL=$(ssh -p 2222 -i "${SSH_KEY}" \
    -o StrictHostKeyChecking=no \
    -o UserKnownHostsFile=/dev/null \
    test@localhost "cat /tmp/import-test" 2>/dev/null)

if [[ "$(echo "${ACTUAL}" | tr -d '\n')" == "managed by terraform" ]]; then
    pass "File content preserved through import cycle"
else
    fail "File content changed after import: ${ACTUAL}"
fi

# Destroy
terraform destroy -auto-approve -var="ssh_private_key_file=${SSH_KEY}"
pass "Post-import destroy succeeded"

rm -f "${WORK_DIR}/terraform.tfstate" "${WORK_DIR}/terraform.tfstate.backup"

# ── Step 14: Test destroy_states ──
step "terraform destroy with destroy_states"

cat > "${WORK_DIR}/main.tf" <<'TFEOF'
terraform {
  required_providers {
    salt = { source = "registry.terraform.io/bartei/salt" }
  }
}

provider "salt" {}

variable "ssh_private_key_file" { type = string }

resource "salt_state" "destroy_test" {
  host        = "localhost"
  port        = 2222
  user        = "test"
  private_key = file(var.ssh_private_key_file)

  states = {
    "create.sls" = <<-SLS
      create_marker:
        file.managed:
          - name: /tmp/destroy-test-marker
          - contents: "resource is active"

      create_artifact:
        file.managed:
          - name: /tmp/destroy-test-artifact
          - contents: "should be removed by destroy states"
    SLS
  }

  destroy_states = {
    "cleanup.sls" = <<-SLS
      remove_artifact:
        file.absent:
          - name: /tmp/destroy-test-artifact

      mark_destroyed:
        file.managed:
          - name: /tmp/destroy-test-marker
          - contents: "resource was destroyed cleanly"
    SLS
  }
}
TFEOF

# Apply — creates both files
terraform apply -auto-approve -var="ssh_private_key_file=${SSH_KEY}"
pass "Apply with destroy_states succeeded"

# Verify both files exist
ssh -p 2222 -i "${SSH_KEY}" \
    -o StrictHostKeyChecking=no \
    -o UserKnownHostsFile=/dev/null \
    test@localhost "test -f /tmp/destroy-test-marker && test -f /tmp/destroy-test-artifact" 2>/dev/null
pass "Both managed files exist on host"

# Destroy — should run destroy_states first, then clean up
terraform destroy -auto-approve -var="ssh_private_key_file=${SSH_KEY}"
pass "Destroy with destroy_states succeeded"

# Verify destroy states ran: artifact should be gone
if ssh -p 2222 -i "${SSH_KEY}" \
    -o StrictHostKeyChecking=no \
    -o UserKnownHostsFile=/dev/null \
    test@localhost "test -f /tmp/destroy-test-artifact" 2>/dev/null; then
    fail "Destroy states did not run — /tmp/destroy-test-artifact still exists"
else
    pass "Destroy states removed /tmp/destroy-test-artifact"
fi

# Verify destroy states ran: marker should show "resource was destroyed cleanly"
MARKER=$(ssh -p 2222 -i "${SSH_KEY}" \
    -o StrictHostKeyChecking=no \
    -o UserKnownHostsFile=/dev/null \
    test@localhost "cat /tmp/destroy-test-marker 2>/dev/null" 2>/dev/null || echo "missing")

if [[ "$(echo "${MARKER}" | tr -d '\n')" == "resource was destroyed cleanly" ]]; then
    pass "Destroy states updated marker: ${MARKER}"
else
    fail "Expected marker to say 'resource was destroyed cleanly', got: ${MARKER}"
fi

# Clean up
ssh -p 2222 -i "${SSH_KEY}" \
    -o StrictHostKeyChecking=no \
    -o UserKnownHostsFile=/dev/null \
    test@localhost "rm -f /tmp/destroy-test-marker /tmp/destroy-test-artifact" 2>/dev/null || true

rm -f "${WORK_DIR}/terraform.tfstate" "${WORK_DIR}/terraform.tfstate.backup"

echo -e "\n${GREEN}${BOLD}All e2e tests passed!${RESET}"
