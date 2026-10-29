data "mtncloud_network" "app" {
  name = "app-net"
}

data "mtncloud_load_balancer_listeners" "web" {
  network_id         = data.mtncloud_network.app.id
  load_balancer_name = "web-lb"
}

output "ports" {
  value = [for l in data.mtncloud_load_balancer_listeners.web.listeners : l.port]
}
