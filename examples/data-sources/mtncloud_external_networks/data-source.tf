# Networks an MTN Router can use to reach the internet.
data "mtncloud_external_networks" "public" {
  name_regex = "^public-"
}

output "external_networks" {
  value = data.mtncloud_external_networks.public.external_networks[*].name
}
