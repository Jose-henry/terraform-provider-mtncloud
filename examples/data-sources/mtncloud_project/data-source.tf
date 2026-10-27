# By the name shown in the console; `id = 123` works too.
data "mtncloud_project" "web" {
  name = "web-prod"
}

output "web_project" {
  value = {
    id    = data.mtncloud_project.web.id
    group = data.mtncloud_project.web.group
    cloud = data.mtncloud_project.web.cloud
  }
}
