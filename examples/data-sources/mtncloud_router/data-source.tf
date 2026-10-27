# A router made in the console, by name; `id = 123` works too.
data "mtncloud_router" "edge" {
  name = "edge"
}

output "edge_public_ip" {
  value = data.mtncloud_router.edge.external_ip
}
