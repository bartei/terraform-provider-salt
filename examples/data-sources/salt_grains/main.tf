# Read Salt grains (system properties) from a remote host
data "salt_grains" "web" {
  host        = "10.0.0.5"
  user        = "deploy"
  private_key = file("~/.ssh/id_ed25519")
}

output "os" {
  value = data.salt_grains.web.values["os"]
}

output "kernel" {
  value = data.salt_grains.web.values["kernel"]
}

output "cpu_count" {
  value = data.salt_grains.web.values["num_cpus"]
}
