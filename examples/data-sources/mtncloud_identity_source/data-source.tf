# By name (matched exactly); `id = 123` works too.
data "mtncloud_identity_source" "example" {
  name = "Corporate AD"
}

output "identity_source_type" {
  value = data.mtncloud_identity_source.example.type
}
