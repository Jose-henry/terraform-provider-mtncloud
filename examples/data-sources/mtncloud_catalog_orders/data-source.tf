data "mtncloud_catalog_orders" "all" {}

output "catalog_orders" {
  value = { for x in data.mtncloud_catalog_orders.all.catalog_orders : x.id => { name = x.name, status = x.status } }
}
