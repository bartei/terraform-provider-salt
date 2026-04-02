---
page_title: "salt_formula Resource - Salt Provider"
subcategory: ""
description: |-
  Clones a Salt formula from a git repository and applies it to a remote host in masterless mode.
---

# salt_formula (Resource)

Clones a Salt formula from a git repository and applies it to a remote host in masterless mode via SSH. The formula is cloned to `/var/lib/salt-tf/formula/` on the target host.

This resource is ideal for applying community formulas from the [Salt Formulas](https://github.com/saltstack-formulas) collection or your own formula repositories.

The resource automatically:
- Installs `git` on the target if not present
- Discovers `init.sls` files in the cloned repository
- Generates a `top.sls` that includes all discovered states
- Supports drift detection via `salt-call test=True`

## Example Usage

### Community formula

```terraform
resource "salt_formula" "nginx" {
  host        = "10.0.0.5"
  user        = "deploy"
  private_key = file("~/.ssh/id_ed25519")

  repo_url     = "https://github.com/saltstack-formulas/nginx-formula.git"
  ref          = "v2.8.1"
  salt_version = "3007"

  pillar = {
    nginx_version = "1.24"
  }
}
```

### Private formula with SSH

```terraform
resource "salt_formula" "internal" {
  host        = "10.0.0.5"
  user        = "deploy"
  private_key = file("~/.ssh/id_ed25519")

  repo_url = "git@github.com:myorg/internal-formula.git"
  ref      = "main"
}
```

-> **Note:** For private repos accessed via SSH, the target host must have access to the git remote (e.g. via a deploy key on the target host).

### Pin to a specific commit

```terraform
resource "salt_formula" "app" {
  host        = "10.0.0.5"
  user        = "deploy"
  private_key = file("~/.ssh/id_ed25519")

  repo_url = "https://github.com/myorg/app-formula.git"
  ref      = "a1b2c3d4e5f6"
}
```

## Argument Reference

### Required

- `host` (String) — Target host address (IP or hostname).
- `user` (String) — SSH user. Must have passwordless sudo access.
- `private_key` (String, Sensitive) — SSH private key contents in PEM format.
- `repo_url` (String) — Git repository URL for the Salt formula. Supports HTTPS and SSH URLs.

### Optional

- `port` (Number) — SSH port. Defaults to `22`.
- `ref` (String) — Git ref to checkout: branch name, tag, or commit SHA. Defaults to the repository's default branch.
- `salt_version` (String) — Salt version to install on the target. Accepts `"3007"`, `"3007.1"`, or `"latest"`.
- `pillar` (Map of String) — Pillar data passed to the formula's Salt states.
- `triggers` (Map of String) — Arbitrary key-value pairs that trigger re-application when changed.
- `ssh_timeout` (Number) — SSH connection timeout in seconds. Defaults to `30`.
- `salt_timeout` (Number) — Timeout in seconds for `salt-call` execution. Defaults to `300`.
- `keep_remote_files` (Boolean) — If `true`, keep formula files on the remote host after `terraform destroy`. Defaults to `false`.

## Attribute Reference

- `id` (String) — Identifier in the format `host:repo_url`.
- `applied_hash` (String) — SHA-256 hash of the last successful `salt-call` JSON output.
- `state_output` (String) — Raw JSON output from the last `salt-call` run.

## Error Responses

### Git clone failure

```
Error: Formula clone failed on 10.0.0.5

git clone failed: command failed: Process exited with status 128
stderr: fatal: repository 'https://github.com/nonexistent/repo.git/' not found
```

### No init.sls found

Occurs when the cloned repository doesn't follow the Salt formula convention (directories containing `init.sls`).

```
Error: Failed to generate top.sls on 10.0.0.5

no init.sls files found in /var/lib/salt-tf/formula — is this a valid Salt formula?
```

### Formula state failure

Same error format as `salt_state` — includes per-state details, SLS file names, and Salt stderr.

## How Formula Discovery Works

The resource scans the cloned repository for directories containing `init.sls` files (excluding `.git/` and `test/` directories). Each discovered formula is added to the auto-generated `top.sls`.

For example, a repository with this structure:

```
myformula/
  init.sls
  config.sls
  service.sls
```

Generates:

```yaml
base:
  '*':
    - myformula
```

Salt then applies `myformula/init.sls`, which can include the other SLS files via Salt's `include` mechanism.

## Import

```shell
terraform import salt_formula.nginx "10.0.0.5:https://github.com/saltstack-formulas/nginx-formula.git"
```

The import ID format is `host:repo_url`. After import, run `terraform apply` to clone the formula and establish the applied hash. See the [`salt_state` import documentation](state.md#import) for the full workflow.
