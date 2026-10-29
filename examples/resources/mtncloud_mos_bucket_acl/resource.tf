resource "mtncloud_mos_bucket" "site" {
  name = "acme-website"
}

# Anyone may list the bucket. Each object also needs a public ACL to be downloaded.
resource "mtncloud_mos_bucket_acl" "site" {
  bucket = mtncloud_mos_bucket.site.name
  acl    = "public-read"
}
