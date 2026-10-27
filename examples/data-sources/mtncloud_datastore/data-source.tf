# By name (matched exactly); `id = 123` works too.
data "mtncloud_datastore" "example" {
  name = "ceph-ssd"
}

output "datastore_free_space" {
  value = data.mtncloud_datastore.example.free_space
}
