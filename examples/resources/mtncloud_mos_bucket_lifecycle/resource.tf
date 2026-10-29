resource "mtncloud_mos_bucket" "site" {
  name = "acme-website"
}

resource "mtncloud_mos_bucket_lifecycle" "site" {
  bucket = mtncloud_mos_bucket.site.name
  rule = [
    {
      id              = "expire-tmp"
      prefix          = "tmp/"
      expiration_days = 7
    },
    {
      id                           = "abandoned-uploads"
      abort_incomplete_upload_days = 2
    },
  ]
}
