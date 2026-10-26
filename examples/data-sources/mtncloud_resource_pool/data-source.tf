data "mtncloud_resource_pool" "mtncloud_pool" {
  name     = "mtncloudpool"
  cloud_id = data.mtncloud_cloud.vspherecloud.id
}