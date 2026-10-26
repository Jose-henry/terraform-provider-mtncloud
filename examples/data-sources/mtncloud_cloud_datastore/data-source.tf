data "mtncloud_cloud" "az1" {
  name = "MTNNG_CLOUD_AZ_1"
}

# Datastores are OpenStack Cinder volume types, for example "__DEFAULT__",
# "ENCR_AT_REST" (encrypted at rest) or "_MULTIATTACH_" (shared volumes).
data "mtncloud_cloud_datastore" "encrypted" {
  cloud_id = data.mtncloud_cloud.az1.id
  name     = "ENCR_AT_REST"
}
