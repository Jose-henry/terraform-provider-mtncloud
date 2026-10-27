data "mtncloud_integrations" "all" {}

output "integrations" {
  value = { for x in data.mtncloud_integrations.all.integrations : x.id => { name = x.name, status = x.status } }
}
