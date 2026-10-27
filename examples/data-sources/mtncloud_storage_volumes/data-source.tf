data "mtncloud_storage_volumes" "all" {}

output "storage_volumes" {
  value = { for x in data.mtncloud_storage_volumes.all.storage_volumes : x.id => { name = x.name, size = x.size } }
}
