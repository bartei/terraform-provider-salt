---
page_title: "salt_grains Data Source - Salt Provider"
subcategory: ""
description: |-
  Reads Salt grains (system properties) from a remote host.
---

# salt_grains (Data Source)

Reads [Salt grains](https://docs.saltproject.io/en/latest/topics/grains/) from a remote host. Grains are system properties collected by Salt, including OS, kernel, CPU, memory, network interfaces, and more.

This data source is useful for making Terraform decisions based on the target host's properties, such as choosing packages by OS family or sizing configurations by available memory.

Salt must already be installed on the target host for this data source to work.

## Example Usage

### Read OS information

```terraform
data "salt_grains" "web" {
  host        = "10.0.0.5"
  user        = "deploy"
  private_key = file("~/.ssh/id_ed25519")
}

output "os" {
  value = data.salt_grains.web.values["os"]
  # => "Debian"
}

output "os_family" {
  value = data.salt_grains.web.values["os_family"]
  # => "Debian"
}

output "kernel" {
  value = data.salt_grains.web.values["kernel"]
  # => "Linux"
}
```

### Conditional logic based on grains

```terraform
data "salt_grains" "target" {
  host        = var.host
  user        = var.user
  private_key = file(var.ssh_key_file)
}

resource "salt_state" "packages" {
  host        = var.host
  user        = var.user
  private_key = file(var.ssh_key_file)

  states = {
    "packages.sls" = data.salt_grains.target.values["os_family"] == "Debian" ? (
      file("${path.module}/salt/packages-debian.sls")
    ) : (
      file("${path.module}/salt/packages-redhat.sls")
    )
  }
}
```

## Argument Reference

### Required

- `host` (String) — Target host address.
- `user` (String) — SSH user. Must have passwordless sudo access.
- `private_key` (String, Sensitive) — SSH private key contents.

### Optional

- `port` (Number) — SSH port. Defaults to `22`.

## Attribute Reference

- `id` (String) — Set to the host address.
- `values` (Map of String) — Map of grain names to their values. All values are converted to strings. Complex grain values (lists, nested objects) are JSON-encoded.

### Common grain keys

| Key | Example | Description |
|-----|---------|-------------|
| `os` | `"Debian"` | Operating system name |
| `os_family` | `"Debian"` | OS family (Debian, RedHat, etc.) |
| `osrelease` | `"12.4"` | OS release version |
| `kernel` | `"Linux"` | Kernel name |
| `kernelrelease` | `"6.1.0-18-amd64"` | Kernel version |
| `cpuarch` | `"x86_64"` | CPU architecture |
| `num_cpus` | `"4"` | Number of CPUs |
| `mem_total` | `"8192"` | Total memory in MB |
| `fqdn` | `"web01.example.com"` | Fully qualified domain name |
| `ip4_interfaces` | `{"eth0":["10.0.0.5"]}` | IPv4 addresses (JSON) |

For the full list of grains, see the [Salt grains documentation](https://docs.saltproject.io/en/latest/ref/modules/all/salt.modules.grains.html).

## Error Responses

### Salt not installed

```
Error: salt-call grains.items failed

Exit code 127.

stderr:
bash: salt-call: command not found
```

Install Salt on the target first, either manually or by using a `salt_state` resource with `salt_version` set.

### SSH connection failure

```
Error: SSH connection to 10.0.0.5 failed

connecting to 10.0.0.5:22: dial tcp 10.0.0.5:22: connect: connection refused
(gave up after 3 retries)
```
