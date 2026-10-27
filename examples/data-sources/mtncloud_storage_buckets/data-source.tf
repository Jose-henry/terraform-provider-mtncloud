data "mtncloud_storage_buckets" "all" {}

output "storage_buckets" {
  value = { for x in data.mtncloud_storage_buckets.all.storage_buckets : x.id => { name = x.name, bucket_name = x.bucket_name } }
}
