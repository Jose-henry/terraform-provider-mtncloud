data "mtncloud_cloud" "vspherecloud" {
  name = "mtncloud_vsphere"
}

data "mtncloud_resource_pool" "mtncloud_pool" {
  name     = "mtncloudpool"
  cloud_id = data.mtncloud_cloud.vspherecloud.id
}

resource "mtncloud_price_set" "tf_example_price_set_software" {
  name             = "terraform-test-everything"
  code             = "terraform-test-everything"
  region_code      = "us-west-2"
  cloud_id         = data.mtncloud_cloud.vspherecloud.id
  resource_pool_id = data.mtncloud_resource_pool.mtncloud_pool.id
  price_unit       = "minute"
  type             = "fixed"
  price_ids        = [mtncloud_price.tf_example_price.id]
}