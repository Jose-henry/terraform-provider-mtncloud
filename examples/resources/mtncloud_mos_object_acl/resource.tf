resource "mtncloud_mos_bucket" "site" {
  name = "acme-website"
}

resource "mtncloud_mos_object" "logo" {
  bucket = mtncloud_mos_bucket.site.name
  key    = "logo.png"
  source = "${path.module}/logo.png"
}

# Anyone may download this one file.
resource "mtncloud_mos_object_acl" "logo" {
  bucket = mtncloud_mos_object.logo.bucket
  key    = mtncloud_mos_object.logo.key
  acl    = "public-read"
}
