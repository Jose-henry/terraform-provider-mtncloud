resource "mtncloud_ansible_tower_integration" "tf_example_ansible_tower_integration" {
  name     = "tfexample ansible tower integration"
  enabled  = true
  url      = "https://ansibletower01.example.com"
  username = "admin"
  password = "password123"
}