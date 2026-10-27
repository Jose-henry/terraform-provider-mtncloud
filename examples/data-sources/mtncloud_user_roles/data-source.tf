data "mtncloud_user_roles" "all" {}

output "user_roles" {
  value = { for x in data.mtncloud_user_roles.all.user_roles : x.id => { name = x.name, name = x.name } }
}
