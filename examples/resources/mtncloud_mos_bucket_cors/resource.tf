resource "mtncloud_mos_bucket" "site" {
  name = "acme-website"
}

resource "mtncloud_mos_bucket_cors" "site" {
  bucket = mtncloud_mos_bucket.site.name
  rule = [
    {
      allowed_origins = ["https://www.example.com"]
      allowed_methods = ["GET", "HEAD"]
      max_age_seconds = 3600
    },
    {
      id              = "browser-uploads"
      allowed_origins = ["https://app.example.com"]
      allowed_methods = ["PUT", "POST"]
      allowed_headers = ["*"]
      expose_headers  = ["ETag"]
    },
  ]
}
