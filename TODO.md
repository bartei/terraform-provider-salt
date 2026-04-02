# TODO — terraform-provider-salt

## Phase 1: Core Foundation (v0.1)

- [x] Project scaffolding (go module, provider skeleton, Makefile)
- [x] SSH client wrapper (`pkg/ssh/client.go`)
- [x] Salt bootstrap logic (`pkg/salt/bootstrap.go`) — updated to new GitHub-hosted bootstrap script
- [x] Salt runner — upload states, run salt-call, parse JSON output (`pkg/salt/runner.go`)
- [x] Auto-generate `top.sls` from uploaded state files
- [x] Fix `salt-call` to run via `sudo` (required for cache directory access)
- [x] Fix pillar root passed as `--pillar-root` CLI flag (was incorrectly a state.apply argument)
- [x] `salt_state` resource — Create, Read, Update, Delete lifecycle
- [x] Pillar data serialization from HCL map to YAML
- [x] Drift detection via `salt-call test=True` in Read() + plan modifiers to trigger re-apply
- [x] Provider-level `salt_version` default config
- [x] Pass provider config (salt_version) through to resources via ConfigureResource
- [x] Unit tests for `pkg/salt/runner.go` (JSON parsing, pillar serialization, top.sls generation)
- [x] Unit tests for `pkg/ssh/client.go` — covered by acceptance tests (thin wrapper, mock adds little value)
- [x] Acceptance tests with a real VM (QEMU + Debian cloud image, `make testacc-vm`)
- [x] End-to-end Terraform test (`make e2e`) — apply, drift detection, re-apply, destroy
- [x] Example configuration (`examples/basic/main.tf`)
- [x] CI pipeline (GitHub Actions: lint, vet, unit tests, build)

## Phase 2: Robustness (v0.2)

- [x] SSH connection retries with exponential backoff (3 retries, 5s→10s→20s)
- [x] Timeout configuration — `ssh_timeout` (default 30s) and `salt_timeout` (default 300s) attributes
- [x] Better error messages — per-state details, stderr, SLS file names in Terraform diagnostics
- [x] Validate Salt version format in schema validation (digits/dots or "latest")
- [x] Support `salt_version = "latest"` — bootstraps Salt without version pinning
- [x] State file cleanup on remote host — `keep_remote_files` attribute (default false)

## Phase 3: Advanced Features (v0.3)

- [x] `salt_formula` resource — clone a Salt formula from git and apply it
- [x] `salt_grains` data source — read grains from a remote host (81 grains, flattened to string map)
- [x] `salt_pillar` data source — render pillar data from a remote host
- ~~Support for Salt environments~~ — handled by Terraform (workspaces, tfvars per env)
- ~~Custom Salt file/pillar roots~~ — not needed; each resource uses `/var/lib/salt-tf/<id>/`
- ~~Parallel state application~~ — Terraform's native parallelism handles this
- [x] Import existing Salt-managed hosts — `terraform import` with passthrough ID

## Phase 4: Polish & Release (v1.0)

- [x] Terraform Registry documentation (docs/ with examples and error reference)
- [ ] Published to Terraform Registry
- [x] GPG-signed releases via GoReleaser (.goreleaser.yml + release workflow)
- [ ] Comprehensive acceptance test suite
- [ ] Example configurations for common use cases (k3s, Docker, Nginx, etc.)
- [ ] Logo and branding for the registry listing

## Ideas / Backlog

- [ ] Skip sudo when SSH user is root — avoids requiring sudo on minimal systems
- [ ] `salt_cmd` resource — run arbitrary `salt-call` commands (escape hatch)
- [ ] Event hooks — trigger external notifications on state apply success/failure
- [ ] Dry-run output in `terraform plan` — show what Salt *would* change
- [ ] Salt master mode — connect to a Salt master API instead of SSH
- [ ] Windows support (WinRM instead of SSH, Salt for Windows bootstrap)
- [ ] Support for encrypted pillar data (Salt's GPG renderer)
