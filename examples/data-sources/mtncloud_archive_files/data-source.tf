data "mtncloud_archive_files" "reports" {
  archive   = "acme-exports"
  folder    = "reports"
  recursive = true
}

output "reports" {
  value = [for f in data.mtncloud_archive_files.reports.files : f.path if !f.is_directory]
}
