---
page_title: "salt_pillar Data Source - Salt Provider"
subcategory: ""
description: |-
  Reads rendered Salt pillar data from a remote host.
---

# salt_pillar (Data Source)

Reads the rendered [Salt pillar](https://docs.saltproject.io/en/latest/topics/pillar/) data from a remote host. Pillar is Salt's mechanism for delivering confidential or host-specific data to minions.

This data source runs `salt-call --local pillar.items` on the target and returns the rendered pillar as a flat string map. It reads from the host's default pillar configuration (typically `/srv/pillar/`).

Salt must already be installed on the target host for this data source to work.

## Example Usage

### Read pillar data

```terraform
data "salt_pillar" "web" {
  host        = "10.0.0.5"
  user        = "deploy"
  private_key = file("~/.ssh/id_ed25519")
}

output "environment" {
  value = data.salt_pillar.web.values["environment"]
  # => "production"
}

output "app_version" {
  value = data.salt_pillar.web.values["app_version"]
  # => "2.1.0"
}
```

### Use pillar data in other resources

```terraform
data "salt_pillar" "target" {
  host        = var.host
  user        = var.user
  private_key = file(var.ssh_key_file)
}

resource "salt_state" "app" {
  host        = var.host
  user        = var.user
  private_key = file(var.ssh_key_file)

  states = {
    "deploy.sls" = file("${path.module}/salt/deploy.sls")
  }

  pillar = {
    # Merge existing pillar data with Terraform-managed values
    environment = data.salt_pillar.target.values["environment"]
    app_version = var.new_app_version
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
- `values` (Map of String) — Map of top-level pillar keys to their values. String values are stored directly. Complex values (dicts, lists) are JSON-encoded.

## Pillar Configuration

This data source reads from the host's standard Salt pillar configuration. To set up pillar data on a host, create files under `/srv/pillar/`:

```
/srv/pillar/
  top.sls          # Maps pillar SLS files to minions
  app.sls          # Pillar data
```

`/srv/pillar/top.sls`:
```yaml
base:
  '*':
    - app
```

`/srv/pillar/app.sls`:
```yaml
environment: production
app_version: 2.1.0
database:
  host: db.internal
  port: 5432
```

The `database` key would be returned as a JSON-encoded string: `{"host":"db.internal","port":5432}`.

## Error Responses

### No pillar data

If no pillar is configured, `values` will be an empty map. This is not an error.

### Salt not installed

```
Error: salt-call pillar.items failed

Exit code 127.

stderr:
bash: salt-call: command not found
```

### Pillar rendering error

If a pillar SLS file has a Jinja or YAML syntax error, Salt returns the error in its output:

```
Error: Failed to parse pillar output

unexpected salt-call output: missing 'local' key
```
