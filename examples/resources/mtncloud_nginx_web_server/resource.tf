resource "mtncloud_nginx_web_server" "this" {
  name             = "web-2"
  project          = "web-prod"
  plan             = "G2S4"
  root_volume_size = 40
  networks         = ["app-net"]
  security_groups  = ["web"]
  web_server_port  = 80
}
