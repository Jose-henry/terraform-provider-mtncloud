# Read-only connectivity check: lists the projects your login can deploy into. Creates nothing.
data "mtncloud_projects" "all" {}

output "project_names" {
  value = data.mtncloud_projects.all.projects[*].name
}
