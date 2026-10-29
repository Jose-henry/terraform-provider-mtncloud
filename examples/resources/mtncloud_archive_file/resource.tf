resource "mtncloud_archive_file" "readme" {
  archive = mtncloud_archive_bucket.exports.name
  path    = "docs/README.txt"
  content = "Nightly exports land here.\n"
}

# By the archive's ID, from a local file: editing the file uploads it again.
resource "mtncloud_archive_file" "report" {
  archive_id = mtncloud_archive_bucket.exports.id
  path       = "reports/2026/october.pdf"
  source     = "${path.module}/october.pdf"
}
