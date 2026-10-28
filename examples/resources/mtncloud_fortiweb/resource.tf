resource "mtncloud_fortiweb" "this" {
  name             = "waf-1"
  project          = "web-prod"
  plan             = "G2S4"
  root_volume_size = 40
  networks         = ["app-net"]
  security_groups  = ["web"]
  floating_ip_pool = "public-network-01"
}
