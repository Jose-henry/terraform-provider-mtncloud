data "mtncloud_network" "app" {
  name = "app-net"
}

data "mtncloud_load_balancer_pool" "web" {
  network_id         = data.mtncloud_network.app.id
  load_balancer_name = "web-lb"
  name               = "web-pool"
}

output "healthy_members" {
  value = [for m in data.mtncloud_load_balancer_pool.web.members : m.address if m.operating_status == "ONLINE"]
}
