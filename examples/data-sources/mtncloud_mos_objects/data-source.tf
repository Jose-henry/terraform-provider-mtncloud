# The top level of a bucket, like a folder listing.
data "mtncloud_mos_objects" "site" {
  bucket    = "acme-website"
  delimiter = "/"
}

output "folders" {
  value = data.mtncloud_mos_objects.site.common_prefixes
}
