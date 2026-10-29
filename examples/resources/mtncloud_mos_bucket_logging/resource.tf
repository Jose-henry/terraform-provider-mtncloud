resource "mtncloud_mos_bucket" "site" {
  name = "acme-website"
}

resource "mtncloud_mos_bucket" "logs" {
  name = "acme-access-logs"
}

resource "mtncloud_mos_bucket_logging" "site" {
  bucket        = mtncloud_mos_bucket.site.name
  target_bucket = mtncloud_mos_bucket.logs.name
  target_prefix = "website/"
}
