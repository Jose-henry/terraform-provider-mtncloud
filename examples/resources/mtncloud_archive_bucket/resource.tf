resource "mtncloud_mos_bucket" "exports" {
  name = "acme-exports"
}

# An archive (Tools > Archives) that keeps its files in that bucket.
resource "mtncloud_archive_bucket" "exports" {
  name          = "acme-exports"
  description   = "Nightly exports"
  mos_bucket_id = mtncloud_mos_bucket.exports.id # or: mos_bucket = mtncloud_mos_bucket.exports.name

  # Optional: let anyone download the files without logging in. Off by default.
  public_url = false
}
