# Everything you can order from the catalog.
data "mtncloud_catalog_items" "all" {}

# Only workflow items whose name mentions storage.
data "mtncloud_catalog_items" "storage" {
  type       = "workflow"
  name_regex = "(?i)storage"
}

output "catalog" {
  value = data.mtncloud_catalog_items.all.items[*].name
}
