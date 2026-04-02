# terraform-provider-salt

A Terraform provider that applies [Salt](https://saltproject.io/) states to remote hosts in **masterless mode** via SSH.

Instead of running a Salt master/minion infrastructure, this provider SSHes into target hosts, ensures Salt is installed, uploads your state files, and runs `salt-call --local state.apply`. Terraform stays the single source of truth — provisioning infrastructure and configuring it in one workflow.

## Features

- **Masterless Salt** — uses `salt-call --local`, no Salt master or minion daemon required
- **Automatic bootstrapping** — installs the specified Salt version on the target if missing
- **Drift detection** — `terraform plan` detects configuration drift via `salt-call test=True`
- *[pkg](pkg)*Pillar data from Terraform** — pass Terraform variables directly as Salt pillar data
- **Trigger-based re-apply** — use `triggers` to force re-application when inputs change

## Requirements

- Terraform >= 1.0
- Go >= 1.25 (for building from source)
- Target hosts must be reachable via SSH

## Usage

```hcl
terraform {
  required_providers {
    salt = {
      source  = "stefanob/salt"
      version = "~> 0.1"
    }
  }
}

provider "salt" {
  salt_version = "3007.1"
}

resource "salt_state" "webserver" {
  host        = "192.168.1.100"
  user        = "root"
  private_key = file("~/.ssh/id_ed25519")

  states = {
    "webserver/init.sls" = file("${path.module}/salt/webserver/init.sls")
    "top.sls"            = file("${path.module}/salt/top.sls")
  }

  pillar = {
    http_port = "8080"
    env       = "production"
  }

  triggers = {
    state_hash = sha256(join("", [
      file("${path.module}/salt/webserver/init.sls"),
      file("${path.module}/salt/top.sls"),
    ]))
  }
}
```

## Provider Configuration

| Attribute      | Type   | Required | Description                                             |
|----------------|--------|----------|---------------------------------------------------------|
| `salt_version` | string | No       | Default Salt version to install on targets.             |

## Resource: `salt_state`

Applies Salt states to a remote host.

### Arguments

| Attribute      | Type        | Required | Description                                                        |
|----------------|-------------|----------|--------------------------------------------------------------------|
| `host`         | string      | Yes      | Target host address.                                               |
| `port`         | number      | No       | SSH port (default: 22).                                            |
| `user`         | string      | Yes      | SSH user.                                                          |
| `private_key`  | string      | Yes      | SSH private key contents (sensitive).                               |
| `salt_version` | string      | No       | Salt version to install. Overrides provider default.               |
| `states`       | map(string) | Yes      | Map of state file paths to contents.                               |
| `pillar`       | map(string) | No       | Pillar data passed to Salt states.                                 |
| `triggers`     | map(string) | No       | Arbitrary values that force re-application when changed.           |

### Read-Only Attributes

| Attribute      | Type   | Description                                          |
|----------------|--------|------------------------------------------------------|
| `id`           | string | Computed resource identifier.                        |
| `applied_hash` | string | Hash of the last successful `salt-call` output.      |
| `state_output` | string | Raw JSON output from the last `salt-call` run.       |

### How It Works

1. **Connect** — establishes an SSH connection to the target host
2. **Bootstrap** — checks if the required Salt version is installed; if not, installs it via the official bootstrap script
3. **Upload** — copies state files and pillar data to `/var/lib/salt-tf/<id>/` on the target
4. **Apply** — runs `salt-call --local --out=json state.apply`
5. **Parse** — captures the JSON output, detects failures, and stores the result hash

On `terraform plan`, the provider runs `salt-call test=True` (dry run) to detect drift — something no `local-exec` approach can do.

### Example with Proxmox

```hcl
resource "proxmox_vm_qemu" "node" {
  name        = "k3s-server"
  target_node = "pve1"
  # ... VM configuration
}

resource "salt_state" "k3s" {
  host        = proxmox_vm_qemu.node.default_ipv4_address
  user        = "root"
  private_key = file("~/.ssh/id_ed25519")

  salt_version = "3007.1"

  states = {
    "k3s/init.sls" = file("${path.module}/salt/k3s/init.sls")
    "top.sls"      = file("${path.module}/salt/top.sls")
  }

  pillar = {
    k3s_version   = var.k3s_version
    cluster_token = random_password.k3s_token.result
    node_ip       = proxmox_vm_qemu.node.default_ipv4_address
  }

  depends_on = [proxmox_vm_qemu.node]
}
```

## Building

```bash
make build
```

## Testing

```bash
# Unit tests
make test

# Acceptance tests (requires real infrastructure)
make testacc
```

## License

MPL-2.0
