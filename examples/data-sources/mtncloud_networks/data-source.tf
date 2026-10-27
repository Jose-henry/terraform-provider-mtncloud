# Networks of one project whose name starts with "app-".
data "mtncloud_networks" "app" {
  project    = "web-prod"
  name_regex = "^app-"
}

output "app_networks" {
  value = { for n in data.mtncloud_networks.app.networks : n.id => "${n.name} ${n.cidr}" }
}
