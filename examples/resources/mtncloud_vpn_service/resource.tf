resource "mtncloud_vpn_service" "this" {
  name             = "vpn-1"
  project          = "web-prod"
  plan             = "G2S4"
  root_volume_size = 40
  networks         = ["app-net"]
  security_groups  = ["web"]
  floating_ip_pool = "public-network-01"
  data_volume_size = 50
}
