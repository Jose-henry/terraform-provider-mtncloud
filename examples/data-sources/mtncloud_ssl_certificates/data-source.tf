data "mtncloud_ssl_certificates" "all" {}

output "ssl_certificates" {
  value = { for x in data.mtncloud_ssl_certificates.all.ssl_certificates : x.id => { name = x.name, domain_name = x.domain_name } }
}
