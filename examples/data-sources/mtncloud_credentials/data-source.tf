data "mtncloud_credentials" "all" {}

output "credentials" {
  value = { for x in data.mtncloud_credentials.all.credentials : x.id => { name = x.name, type = x.type } }
}
