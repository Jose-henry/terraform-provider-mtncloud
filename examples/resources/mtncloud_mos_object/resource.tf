resource "mtncloud_mos_bucket" "site" {
  name = "acme-website"
}

resource "mtncloud_mos_object" "index" {
  bucket = mtncloud_mos_bucket.site.name
  key    = "index.html"
  source = "${path.module}/site/index.html" # editing the file uploads it again
}

resource "mtncloud_mos_object" "config" {
  bucket   = mtncloud_mos_bucket.site.name
  key      = "config.json"
  content  = jsonencode({ env = "prod" })
  metadata = { owner = "web-team" }
}

# Kept from deletion until the date (the bucket needs object_lock_enabled).
resource "mtncloud_mos_object" "report" {
  bucket         = "acme-records"
  key            = "reports/2026.pdf"
  source         = "${path.module}/reports/2026.pdf"
  retention_mode = "GOVERNANCE"
  retain_until   = "2027-12-31T00:00:00Z"
}
