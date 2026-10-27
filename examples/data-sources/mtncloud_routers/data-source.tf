# Every MTN Router you can see, including those made in the console.
data "mtncloud_routers" "all" {}

# Only routers whose name starts with "edge".
data "mtncloud_routers" "edge" {
  name_regex = "^edge"
}

output "router_public_ips" {
  value = { for r in data.mtncloud_routers.all.routers : r.name => r.external_ip }
}
