data "mtncloud_network" "app" {
  name = "app-net"
}

data "mtncloud_load_balancer_health_monitors" "web" {
  network_id         = data.mtncloud_network.app.id
  load_balancer_name = "web-lb"
}

output "checks" {
  value = { for h in data.mtncloud_load_balancer_health_monitors.web.health_monitors : h.pool_name => "${h.type} every ${h.delay}s" }
}
