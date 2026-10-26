# Read-only connectivity check: looks up the availability-zone cloud and its projects
# (resource pools). Creates nothing.
data "mtncloud_cloud" "az1" {
  name = var.cloud_name
}

data "mtncloud_resource_pool" "projects" {
  cloud_id = data.mtncloud_cloud.az1.id
}

output "cloud_id" {
  value = data.mtncloud_cloud.az1.id
}
