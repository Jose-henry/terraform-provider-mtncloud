data "mtncloud_identity_sources" "all" {}

output "identity_sources" {
  value = { for x in data.mtncloud_identity_sources.all.identity_sources : x.id => { name = x.name, type = x.type } }
}
