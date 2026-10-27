# Every instance in one project.
data "mtncloud_instances" "web" {
  project = "web-prod"
}

# Only instances whose name starts with "app-", in any project.
data "mtncloud_instances" "apps" {
  name_regex = "^app-"
}

output "web_private_ips" {
  value = { for i in data.mtncloud_instances.web.instances : i.name => i.private_ip }
}
