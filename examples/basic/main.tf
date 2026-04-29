terraform {
  required_providers {
    salt = {
      source = "registry.terraform.io/bartei/salt"
    }
  }
}

provider "salt" {
  # Default to whatever the salt-bootstrap script provides for the target
  # distro. Pin a specific version (e.g. "3007") if your fleet is uniform
  # and you know that version is available on every target's distro.
  salt_version = "latest"
}

variable "ssh_host" {
  description = "Target host"
  type        = string
  default     = "localhost"
}

variable "ssh_port" {
  description = "SSH port"
  type        = number
  default     = 2222
}

variable "ssh_user" {
  description = "SSH user"
  type        = string
  default     = "test"
}

variable "ssh_private_key_file" {
  description = "Path to SSH private key"
  type        = string
}

variable "greeting" {
  description = "Greeting message to write to the managed file"
  type        = string
  default     = "Hello from Terraform + Salt!"
}

# Apply a Salt state that creates a file with a greeting from pillar data.
resource "salt_state" "example" {
  host        = var.ssh_host
  port        = var.ssh_port
  user        = var.ssh_user
  private_key = file(var.ssh_private_key_file)

  states = {
    "greeting.sls" = <<-SLS
      managed_greeting:
        file.managed:
          - name: /tmp/terraform-salt-example
          - contents: {{ pillar['greeting'] }}
    SLS
  }

  pillar = {
    greeting = var.greeting
  }
}

output "state_id" {
  value = salt_state.example.id
}

output "applied_hash" {
  value = salt_state.example.applied_hash
}
