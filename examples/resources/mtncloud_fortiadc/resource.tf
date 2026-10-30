resource "mtncloud_fortiadc" "this" {
  name             = "adc-1"
  project          = "web-prod"
  plan             = "G4S16"
  root_volume_size = 30
  networks         = ["app-net"]
  security_groups  = ["web"]
  floating_ip_pool = "public-network-01"
}
