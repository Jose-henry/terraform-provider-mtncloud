resource "mtncloud_fortimanager" "this" {
  name             = "fmg-1"
  project          = "web-prod"
  plan             = "G4S8"
  root_volume_size = 500
  networks         = ["app-net"]
  security_groups  = ["web"]
  floating_ip_pool = "public-network-01"
}
