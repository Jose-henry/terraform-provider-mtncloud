data "mtncloud_network" "app" {
  name = "app-net"
}

data "mtncloud_load_balancer_member" "web1" {
  network_id         = data.mtncloud_network.app.id
  load_balancer_name = "web-lb"
  pool_name          = "web-pool"
  name               = "web-1"
}

output "web1_status" {
  value = data.mtncloud_load_balancer_member.web1.operating_status
}
