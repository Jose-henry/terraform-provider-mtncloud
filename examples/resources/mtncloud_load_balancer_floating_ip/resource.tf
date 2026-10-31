# A new public address from a pool the load balancer's network reaches through its router.
resource "mtncloud_load_balancer_floating_ip" "web" {
  network_id       = mtncloud_network.app.id
  load_balancer_id = mtncloud_load_balancer.web.id
  floating_ip_pool = "public-network-02"

  # Or an address the project already holds, by the IP itself (changing it swaps the address in place):
  # floating_ip_address = "203.0.113.9"
}

output "public_ip" {
  value = mtncloud_load_balancer_floating_ip.web.address
}
