# Anyone with the URL can download the report, without logging in, for one day.
resource "mtncloud_archive_file_link" "report" {
  file_id        = mtncloud_archive_file.report.id
  expire_seconds = 86400
}

output "report_url" {
  value     = mtncloud_archive_file_link.report.download_url
  sensitive = true
}
