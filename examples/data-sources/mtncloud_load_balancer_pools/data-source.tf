data "mtncloud_network" "app" {
  name = "app-net"
}

data "mtncloud_load_balancer_pools" "web" {
  network_id         = data.mtncloud_network.app.id
  load_balancer_name = "web-lb"
}

output "pool_algorithms" {
  value = { for p in data.mtncloud_load_balancer_pools.web.pools : p.name => p.algorithm }
}
