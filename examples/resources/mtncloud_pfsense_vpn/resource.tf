# PfSense needs two networks (inside and outside). Its disks are fixed by MTN Cloud (a 50 GB root
# disk), and it has its own plans: "Small (G2S4)", "Medium (G4S4)" or "Large (G4S8)", by name or code.
resource "mtncloud_pfsense_vpn" "this" {
  name             = "vpn-1"
  project          = "web-prod"
  plan             = "G2S4"
  networks         = ["app-net", "dmz-net"]
  floating_ip_pool = "public-network-01"

  lifecycle {
    # A changed input orders the VPN again: guard against deleting it by accident.
    prevent_destroy = true
  }
}

