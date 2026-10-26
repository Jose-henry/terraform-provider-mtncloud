# Every project you can deploy into, by the name you gave it.
data "mtncloud_projects" "all" {}

# Only projects whose name starts with "web".
data "mtncloud_projects" "web" {
  name_regex = "^web"
}

output "project_names" {
  value = data.mtncloud_projects.all.projects[*].name
}
