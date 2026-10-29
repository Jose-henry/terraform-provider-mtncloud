data "mtncloud_archive_buckets" "all" {}

output "archive_names" {
  value = [for a in data.mtncloud_archive_buckets.all.archives : a.name]
}
