# Where a project can store volumes (OpenStack Cinder volume types).
data "mtncloud_datastores" "web" {
  project = "web-prod"
}

output "datastores" {
  value = data.mtncloud_datastores.web.datastores[*].name
}
