# Apply a Salt formula from a git repository
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
