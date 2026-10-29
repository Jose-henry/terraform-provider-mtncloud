data "mtncloud_network" "app" {
  name = "app-net"
}

data "mtncloud_load_balancer_listener" "https" {
  network_id         = data.mtncloud_network.app.id
  load_balancer_name = "web-lb"
  name               = "https"
}

output "https_pool" {
  value = data.mtncloud_load_balancer_listener.https.default_pool_id
}
