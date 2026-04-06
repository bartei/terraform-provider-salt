---
page_title: "Salt Provider"
subcategory: ""
description: |-
  The Salt provider applies Salt states to remote hosts in masterless mode via SSH.
---

# Salt Provider

The Salt provider manages configuration on remote Linux hosts using [Salt](https://docs.saltproject.io/) in masterless mode. It connects via SSH, uploads Salt states, and runs `salt-call --local` to apply them — no Salt master required.

## How it works

1. **Connect** — establishes an SSH connection (with retries and exponential backoff)
2. **Bootstrap** — installs the requested Salt version if not already present
3. **Upload** — copies state files and pillar data to `/var/lib/salt-tf/<resource_id>/`
4. **Apply** — runs `sudo salt-call --local state.apply` with JSON output
5. **Parse** — captures per-state results, detects failures, stores a hash for drift detection

On subsequent `terraform plan`, the provider runs `salt-call test=True` to detect drift. If the remote host has changed, Terraform will show the resource as needing an update.

On `terraform destroy`, the provider optionally applies **destroy states** (to reverse changes like stopping services or unmounting filesystems) before cleaning up remote files.

## Authentication

The provider uses **SSH key-based authentication only**. Pass the private key contents directly via the `private_key` attribute (typically using Terraform's `file()` function). Password authentication, SSH agent forwarding, and bastion hosts are not supported.

The SSH user must have **passwordless sudo** access, as `salt-call` requires root privileges.

## Example Usage

```terraform
terraform {
  required_providers {
    salt = {
      source  = "bartei/salt"
      version = "~> 0.1"
    }
  }
}

provider "salt" {
  salt_version = "3007"
}

resource "salt_state" "webserver" {
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

## Provider Configuration

### Optional

- `salt_version` (String) — Default Salt version to install on targets. Can be overridden per resource. Use a version number like `"3007"` or `"latest"`.

## Error Handling

The provider surfaces detailed error information from Salt without requiring changes to the Terraform log level. Errors include:

- **Host identification** — which host the error occurred on
- **Salt state details** — the name, SLS file, and comment for each failed state
- **Jinja rendering errors** — the exact line and variable that caused the failure, with Salt's `<=====` pointer
- **Salt stderr** — warnings, tracebacks, and error logs from salt-call
- **Drift summaries** — per-state diffs showing what changed on the remote host

### Example error output

```
Error: Salt apply failed on 10.0.0.5

  with salt_state.webserver,
  on main.tf line 12, in resource "salt_state" "webserver":

Salt returned an error:
Rendering SLS 'base:nginx' failed: Jinja variable 'dict object' has no
attribute 'server_name'; line 8

---
[...]
    - contents: {{ pillar['server_name'] }}    <======================
---

stderr:
[ERROR   ] Rendering exception occurred
jinja2.exceptions.UndefinedError: 'dict object' has no attribute 'server_name'
```

### Example drift warning

```
Warning: Drift detected on 10.0.0.5

Changed (1):
  ~ /etc/nginx/nginx.conf (sls: nginx)
    Comment: The file /etc/nginx/nginx.conf is set to be changed
    diff: ---
+++
@@ -1 +1 @@
-old content
+managed content
```

## Remote File Layout

Each resource instance stores its files in an isolated directory on the remote host:

```
/var/lib/salt-tf/
  <resource_id>/           # salt_state resources
    top.sls                # auto-generated from state file names
    nginx.sls              # uploaded state files
    pillar/
      top.sls
      custom.sls           # pillar data serialized from HCL
  formula/                 # salt_formula resources
    <cloned git repo>/
    top.sls
    pillar/
```

This layout ensures multiple `salt_state` resources targeting the same host do not interfere with each other. Files persist across reboots and are cleaned up on `terraform destroy` (unless `keep_remote_files = true`).
