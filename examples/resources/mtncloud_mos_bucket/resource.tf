# A bucket, listed in the console under Infrastructure > Storage > Buckets.
resource "mtncloud_mos_bucket" "backups" {
  name          = "acme-backups"
  console_name  = "Acme backups"
  force_destroy = false # destroy fails while it holds objects
}

# Bucket names are shared by every MTN Cloud tenant. name_prefix picks a free one, such as
# "acme-logs-k3x9q2m7", when the bucket is created, and keeps it.
resource "mtncloud_mos_bucket" "logs" {
  name_prefix = "acme-logs-"
}

# A bucket whose objects can be kept from deletion (it can't be turned off later).
resource "mtncloud_mos_bucket" "records" {
  name                = "acme-records"
  object_lock_enabled = true
}

output "endpoint" {
  value = mtncloud_mos_bucket.backups.endpoint_url
}
