data "mtncloud_mos_object" "config" {
  bucket          = "acme-website"
  key             = "config.json"
  include_content = true
}

output "env" {
  value = jsondecode(data.mtncloud_mos_object.config.content).env
}
