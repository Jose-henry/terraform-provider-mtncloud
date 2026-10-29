data "mtncloud_mos_buckets" "all" {}

output "bucket_names" {
  value = data.mtncloud_mos_buckets.all.buckets[*].name
}
