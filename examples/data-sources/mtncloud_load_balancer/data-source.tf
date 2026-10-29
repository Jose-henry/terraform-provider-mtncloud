data "mtncloud_network" "app" {
  name = "app-net"
}

# By name (matched exactly on the network); `id = "..."` works too.
data "mtncloud_load_balancer" "web" {
  network_id = data.mtncloud_network.app.id
  name       = "web-lb"
}

output "web_listeners" {
  value = { for l in data.mtncloud_load_balancer.web.listeners : l.name => "${l.protocol}:${l.port}" }
}

output "web_backends" {
  value = flatten([for p in data.mtncloud_load_balancer.web.pools : [for m in p.members : "${m.address}:${m.port}"]])
}
