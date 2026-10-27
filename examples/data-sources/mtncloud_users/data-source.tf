data "mtncloud_users" "all" {}

output "users" {
  value = { for x in data.mtncloud_users.all.users : x.username => x.username }
}
