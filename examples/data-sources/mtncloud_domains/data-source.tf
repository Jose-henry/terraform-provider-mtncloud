data "mtncloud_domains" "all" {}

output "domains" {
  value = { for x in data.mtncloud_domains.all.domains : x.id => { name = x.name, fqdn = x.fqdn } }
}
