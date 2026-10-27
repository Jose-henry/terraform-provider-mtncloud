# By name (matched exactly); `id = 123` works too.
data "mtncloud_catalog_order" "example" {
  name = "web-prod"
}

output "catalog_order_status" {
  value = data.mtncloud_catalog_order.example.status
}
