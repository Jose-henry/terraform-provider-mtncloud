data "mtncloud_network" "app" {
  name = "app-net"
}

data "mtncloud_load_balancer_l7_policies" "http" {
  network_id         = data.mtncloud_network.app.id
  load_balancer_name = "web-lb"
  listener_name      = "http"
}

output "policies" {
  value = [for p in data.mtncloud_load_balancer_l7_policies.http.l7_policies : "${p.position}: ${p.action}"]
}
