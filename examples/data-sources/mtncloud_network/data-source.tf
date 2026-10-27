# A network made in the console, by name. Add project if the name is used in several projects.
data "mtncloud_network" "app" {
  name    = "app-net"
  project = "web-prod"
}

output "app_network" {
  value = {
    id      = data.mtncloud_network.app.id
    cidr    = data.mtncloud_network.app.cidr
    project = data.mtncloud_network.app.project
    cloud   = data.mtncloud_network.app.cloud
  }
}
