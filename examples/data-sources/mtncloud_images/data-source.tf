# Operating system images. Applications (Kubernetes, databases, VPN...) are in the catalog:
# see mtncloud_catalog_items.
data "mtncloud_images" "all" {}

data "mtncloud_images" "ubuntu" {
  name_regex = "Ubuntu"
}

output "images" {
  value = { for i in data.mtncloud_images.all.images : i.code => i.name }
}
