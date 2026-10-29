data "mtncloud_archive_bucket" "exports" {
  name = "acme-exports"
}

output "files" {
  value = data.mtncloud_archive_bucket.exports.file_count
}
