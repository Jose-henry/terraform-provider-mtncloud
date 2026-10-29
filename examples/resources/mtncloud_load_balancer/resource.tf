resource "mtncloud_load_balancer" "web" {
  network_id = mtncloud_network.app.id
  name       = "web"

  # Optional: the VIP address on the network's subnet. Left out, the cloud picks one.
  vip_address = "10.0.0.20"
  tags        = ["team-web"]
}

output "vip" {
  value = mtncloud_load_balancer.web.vip_address
}
