data "mtncloud_network" "app" {
  name = "app-net"
}

data "mtncloud_load_balancer_members" "web" {
  network_id         = data.mtncloud_network.app.id
  load_balancer_name = "web-lb"
  pool_name          = "web-pool"
}

output "members" {
  value = { for m in data.mtncloud_load_balancer_members.web.members : m.address => m.operating_status }
}
