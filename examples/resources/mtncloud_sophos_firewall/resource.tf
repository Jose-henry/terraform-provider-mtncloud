# SOPHOS (bring your own licence) needs at least 2 networks, an 80 GB root disk, and 2 vCPU with
# 4 GB of memory.
resource "mtncloud_sophos_firewall" "this" {
  name             = "fw-1"
  project          = "web-prod"
  plan             = "G2S4"
  root_volume_size = 80
  networks         = ["app-net", "dmz-net"]
  floating_ip_pool = "public-network-01"
}
