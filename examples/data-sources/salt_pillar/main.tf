# Read Salt pillar data from a remote host
data "salt_pillar" "web" {
  host        = "10.0.0.5"
  user        = "deploy"
  private_key = file("~/.ssh/id_ed25519")
}

output "environment" {
  value = data.salt_pillar.web.values["environment"]
}
