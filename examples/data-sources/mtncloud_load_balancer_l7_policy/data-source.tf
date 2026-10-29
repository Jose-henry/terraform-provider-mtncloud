data "mtncloud_network" "app" {
  name = "app-net"
}

data "mtncloud_load_balancer_l7_policy" "api" {
  network_id         = data.mtncloud_network.app.id
  load_balancer_name = "web-lb"
  listener_name      = "http"
  name               = "api"
}

output "api_rules" {
  value = data.mtncloud_load_balancer_l7_policy.api.rules
}
