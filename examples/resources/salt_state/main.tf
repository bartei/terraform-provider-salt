# Apply Salt states to a remote host
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

      /etc/nginx/conf.d/app.conf:
        file.managed:
          - contents: |
              server {
                listen 80;
                server_name {{ pillar['server_name'] }};
                location / { proxy_pass http://localhost:{{ pillar['app_port'] }}; }
              }
          - require:
            - pkg: nginx
    SLS
  }

  pillar = {
    server_name = "app.example.com"
    app_port    = "8080"
  }
}
