# By name (matched exactly); `id = 123` works too.
data "mtncloud_app" "example" {
  name = "my-app"
}

output "app_instances" {
  value = data.mtncloud_app.example.instances
}
