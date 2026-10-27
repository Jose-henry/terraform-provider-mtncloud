# By name (matched exactly); `id = 123` works too.
data "mtncloud_user" "example" {
  username = "jane"
}

output "user_email" {
  value = data.mtncloud_user.example.email
}
