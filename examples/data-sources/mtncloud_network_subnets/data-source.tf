# Only those whose name matches; leave name_regex out to list them all.
data "mtncloud_network_subnets" "all" {
  name_regex = "^app-"
}

output "network_subnets" {
  value = { for x in data.mtncloud_network_subnets.all.network_subnets : x.id => { name = x.name, cidr = x.cidr } }
}
