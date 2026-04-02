---
page_title: "salt_state Resource - Salt Provider"
subcategory: ""
description: |-
  Applies Salt states to a remote host in masterless mode via SSH.
---

# salt_state (Resource)

Applies Salt states to a remote host in masterless mode via SSH. States are defined inline as HCL strings and uploaded to the target host before applying.

The resource supports:
- **Pillar data** passed from Terraform variables into Salt's Jinja templates
- **Drift detection** via `salt-call test=True` on every `terraform plan`
- **Idempotent applies** — running `terraform apply` twice produces no changes if the host is in sync
- **Automatic cleanup** of remote files on `terraform destroy`

## Example Usage

### Basic — install and start nginx

```terraform
resource "salt_state" "nginx" {
  host        = "10.0.0.5"
  user        = "deploy"
  private_key = file("~/.ssh/id_ed25519")

  states = {
    "nginx.sls" = <<-SLS
      nginx:
        pkg.installed: []
        service.running:
          - enable: True
    SLS
  }
}
```

### With pillar data

```terraform
resource "salt_state" "app_config" {
  host        = "10.0.0.5"
  user        = "deploy"
  private_key = file("~/.ssh/id_ed25519")

  states = {
    "app.sls" = <<-SLS
      /etc/myapp/config.yml:
        file.managed:
          - contents: |
              database: {{ pillar['db_host'] }}
              port: {{ pillar['db_port'] }}
    SLS
  }

  pillar = {
    db_host = var.database_host
    db_port = "5432"
  }
}
```

### Multiple state files

```terraform
resource "salt_state" "k3s" {
  host        = "10.0.0.5"
  user        = "deploy"
  private_key = file("~/.ssh/id_ed25519")

  states = {
    "k3s/init.sls"   = file("${path.module}/salt/k3s/init.sls")
    "k3s/config.sls"  = file("${path.module}/salt/k3s/config.sls")
    "k3s/service.sls" = file("${path.module}/salt/k3s/service.sls")
  }

  pillar = {
    k3s_version = "v1.28.4+k3s1"
  }

  salt_version = "3007"
  salt_timeout = 600  # k3s install can be slow
}
```

### Re-apply on external change

```terraform
resource "salt_state" "config" {
  host        = "10.0.0.5"
  user        = "deploy"
  private_key = file("~/.ssh/id_ed25519")

  states = {
    "config.sls" = file("${path.module}/salt/config.sls")
  }

  triggers = {
    config_hash = filemd5("${path.module}/salt/config.sls")
  }
}
```

### Multiple resources on the same host

Each `salt_state` resource uses an isolated working directory (`/var/lib/salt-tf/<id>/`), so multiple resources can safely target the same host without interfering with each other:

```terraform
resource "salt_state" "base" {
  host        = "10.0.0.5"
  user        = "deploy"
  private_key = file("~/.ssh/id_ed25519")

  states = {
    "base.sls" = file("${path.module}/salt/base.sls")
  }
}

resource "salt_state" "app" {
  host        = "10.0.0.5"
  user        = "deploy"
  private_key = file("~/.ssh/id_ed25519")

  states = {
    "app.sls" = file("${path.module}/salt/app.sls")
  }

  depends_on = [salt_state.base]
}
```

## Argument Reference

### Required

- `host` (String) — Target host address (IP or hostname).
- `user` (String) — SSH user. Must have passwordless sudo access.
- `private_key` (String, Sensitive) — SSH private key contents in PEM format. Use `file()` to read from disk.
- `states` (Map of String) — Map of state file paths to their contents. Keys are relative paths (e.g. `"nginx.sls"`, `"k3s/init.sls"`). A `top.sls` is generated automatically.

### Optional

- `port` (Number) — SSH port. Defaults to `22`.
- `salt_version` (String) — Salt version to install on the target. Accepts a version number like `"3007"` or `"3007.1"`, or `"latest"` to install without version pinning. Overrides the provider-level `salt_version`.
- `pillar` (Map of String) — Pillar data passed to Salt states. Values are available in Jinja templates as `{{ pillar['key'] }}`.
- `triggers` (Map of String) — Arbitrary key-value pairs. Any change to this map triggers re-application of states on the next `terraform apply`.
- `ssh_timeout` (Number) — SSH connection timeout in seconds. Defaults to `30`.
- `salt_timeout` (Number) — Timeout in seconds for `salt-call` execution. Defaults to `300` (5 minutes). Set to `0` for no timeout.
- `keep_remote_files` (Boolean) — If `true`, keep Salt state files on the remote host after `terraform destroy`. Defaults to `false`.

## Attribute Reference

- `id` (String) — Deterministic identifier derived from the host and state file names.
- `applied_hash` (String) — SHA-256 hash of the last successful `salt-call` JSON output. Changes when drift is detected.
- `state_output` (String) — Raw JSON output from the last `salt-call` run. Contains per-state results including changes, comments, and timing.

## Error Responses

### SSH connection failure

Occurs when the host is unreachable or the SSH key is rejected. The provider retries 3 times with exponential backoff (5s, 10s, 20s) before failing.

```
Error: SSH connection to 10.0.0.5 failed

connecting to 10.0.0.5:22: dial tcp 10.0.0.5:22: connect: connection refused
(gave up after 3 retries)
```

### Salt bootstrap failure

Occurs when the requested Salt version cannot be installed.

```
Error: Salt bootstrap failed on 10.0.0.5

Failed to install Salt version "9999".

bootstrap failed: command failed: Process exited with status 1
stderr: [ERROR] Unable to install version 9999
```

### Salt state failure

Occurs when one or more Salt states fail to apply. The error includes the state name, SLS file, and Salt's comment.

```
Error: Salt states failed on 10.0.0.5

  ~ nginx (sls: webserver)
    Comment: Package nginx failed to install
```

### Jinja rendering error

Occurs when a Salt state template references undefined variables or has syntax errors.

```
Error: Salt apply failed on 10.0.0.5

Salt returned an error:
Rendering SLS 'base:app' failed: Jinja variable 'dict object' has no
attribute 'missing_key'; line 5

---
    - contents: {{ pillar['missing_key'] }}    <======================
---

stderr:
[ERROR   ] Rendering exception occurred
jinja2.exceptions.UndefinedError: 'dict object' has no attribute 'missing_key'
```

### Salt timeout

Occurs when `salt-call` exceeds the configured `salt_timeout`.

```
Error: Salt apply failed on 10.0.0.5

salt-call exited with code 124 and no JSON output.

stderr:
```

Exit code 124 indicates the `timeout` command killed the process.

## Drift Detection

On every `terraform plan`, the provider connects to the host and runs `salt-call test=True` (dry run). If Salt reports any state would make changes, the resource is marked for update.

Drift is shown as a Terraform warning with per-state details:

```
Warning: Drift detected on 10.0.0.5

Changed (1):
  ~ /etc/nginx/nginx.conf (sls: nginx)
    Comment: The file /etc/nginx/nginx.conf is set to be changed
    diff: ---
+++
@@ -1 +1 @@
-tampered content
+managed content
```

If the host is unreachable during `terraform plan`, the resource is removed from state and will be recreated on the next `terraform apply`.

## Import

Existing Salt-managed hosts can be imported into Terraform state. This is useful when you want to start managing a host that was previously configured manually or by another tool.

### Import workflow

1. Write the full `salt_state` resource block in your HCL configuration (host, user, private_key, states, pillar — everything)
2. Run `terraform import` with the resource ID
3. Run `terraform plan` — it will show the resource needs an update (to establish the applied hash)
4. Run `terraform apply` — Salt re-applies the states and records the hash
5. Subsequent plans will show no changes if the host is in sync

### Usage

```shell
terraform import salt_state.webserver <resource_id>
```

The `<resource_id>` is a deterministic hash derived from the host and state file names. If you previously managed this resource with Terraform, the ID is shown in `terraform show` or `terraform state list`. If you don't know the ID, you can use any unique string — it will be replaced by the computed ID on the first apply.

### Example

```shell
# Import with the known resource ID
terraform import salt_state.webserver abc123def456

# Or import with an arbitrary identifier
terraform import salt_state.webserver my-webserver
```

After import, `terraform plan` will show:

```
  # salt_state.webserver will be updated in-place
  ~ resource "salt_state" "webserver" {
      + applied_hash = (known after apply)
        id           = "my-webserver"
      + state_output = (known after apply)
        # (10 unchanged attributes hidden)
    }
```

Running `terraform apply` will connect to the host, apply the states, and record the hash. After that, drift detection works normally.
