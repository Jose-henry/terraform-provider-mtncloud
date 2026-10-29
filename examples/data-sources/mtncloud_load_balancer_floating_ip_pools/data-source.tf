data "mtncloud_network" "app" {
  name = "app-net"
}

data "mtncloud_load_balancer_floating_ip_pools" "app" {
  network_id = data.mtncloud_network.app.id
}

output "public_pools" {
  value = [for p in data.mtncloud_load_balancer_floating_ip_pools.app.pools : p.name]
}
