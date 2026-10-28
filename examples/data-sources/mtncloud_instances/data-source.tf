# Every instance in one project.
data "mtncloud_instances" "web" {
  project = "web-prod"
}

# Only instances whose name starts with "app-", in any project.
data "mtncloud_instances" "apps" {
  name_regex = "^app-"
}

# Every instance with a card on the "app-net" network, primary or not.
data "mtncloud_instances" "on_app_net" {
  project = "web-prod"
  network = "app-net"
}

output "web_private_ips" {
  value = { for i in data.mtncloud_instances.web.instances : i.name => i.private_ip }
}
