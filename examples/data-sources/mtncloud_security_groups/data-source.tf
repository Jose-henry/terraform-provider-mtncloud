# Security groups in a project, including the "default" group every project gets.
data "mtncloud_security_groups" "web" {
  project = "web-prod"
}

data "mtncloud_security_groups" "default" {
  project    = "web-prod"
  name_regex = "^default$"
}

output "default_security_group_id" {
  value = data.mtncloud_security_groups.default.security_groups[0].id
}
