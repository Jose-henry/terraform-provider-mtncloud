# The tenant's keys, for S3-compatible tools. They are stored in state: protect it.
data "mtncloud_mos_credentials" "this" {}

output "mos_endpoint" {
  value = data.mtncloud_mos_credentials.this.public_endpoint
}
