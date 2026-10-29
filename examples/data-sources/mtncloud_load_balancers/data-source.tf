data "mtncloud_network" "app" {
  name = "app-net"
}

# Every load balancer on the network; add name or status to narrow it.
data "mtncloud_load_balancers" "app" {
  network_id = data.mtncloud_network.app.id
  status     = "ACTIVE"
}

output "load_balancer_addresses" {
  value = { for lb in data.mtncloud_load_balancers.app.load_balancers : lb.name => coalesce(lb.floating_ip, lb.vip_address) }
}
