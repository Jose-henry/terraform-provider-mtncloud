data "mtncloud_network" "app" {
  name = "app-net"
}

data "mtncloud_load_balancer_health_monitor" "web" {
  network_id         = data.mtncloud_network.app.id
  load_balancer_name = "web-lb"
  name               = "web-check"
}

output "check_path" {
  value = data.mtncloud_load_balancer_health_monitor.web.url_path
}
