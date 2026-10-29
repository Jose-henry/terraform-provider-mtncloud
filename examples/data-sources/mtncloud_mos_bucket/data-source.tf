# Everything about one bucket: console record, ACL, CORS, lifecycle, logging, object lock.
data "mtncloud_mos_bucket" "site" {
  name = "acme-website"
}

output "site_bucket" {
  value = {
    acl             = data.mtncloud_mos_bucket.site.acl
    endpoint        = data.mtncloud_mos_bucket.site.endpoint_url
    lifecycle_rules = length(data.mtncloud_mos_bucket.site.lifecycle_rule)
    logs_go_to      = data.mtncloud_mos_bucket.site.logging_target_bucket
  }
}
