resource "mtncloud_mos_bucket" "records" {
  name                = "acme-records"
  object_lock_enabled = true
}

# Every new object is kept for 90 days.
resource "mtncloud_mos_bucket_object_lock" "records" {
  bucket = mtncloud_mos_bucket.records.name
  days   = 90
}
