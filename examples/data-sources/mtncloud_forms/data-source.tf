data "mtncloud_forms" "all" {}

output "forms" {
  value = { for x in data.mtncloud_forms.all.forms : x.id => { name = x.name, code = x.code } }
}
