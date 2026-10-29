# A bucket, listed in the console under Infrastructure > Storage > Buckets.
resource "mtncloud_mos_bucket" "backups" {
  name          = "acme-backups"
  console_name  = "Acme backups"
  force_destroy = false # destroy fails while it holds objects
}

# A bucket whose objects can be kept from deletion (it can't be turned off later).
resource "mtncloud_mos_bucket" "records" {
  name                = "acme-records"
  object_lock_enabled = true
}

output "endpoint" {
  value = mtncloud_mos_bucket.backups.endpoint_url
}
