# Where a project can store volumes.
data "mtncloud_datastores" "web" {
  project = "web-prod"
}

output "datastores" {
  value = data.mtncloud_datastores.web.datastores[*].name
}
