data "mtncloud_load_balancers" "all" {}

output "load_balancers" {
  value = { for x in data.mtncloud_load_balancers.all.load_balancers : x.id => { name = x.name, external_ip = x.external_ip } }
}
