variable "admin_password" {
  type      = string
  sensitive = true
}

resource "mtncloud_managed_streaming" "this" {
  name             = "events-kafka"
  project          = "web-prod"
  plan             = "G2S4"
  root_volume_size = 100
  networks         = ["app-net"]
  security_groups  = ["web"]
  admin_username   = "admin"
  admin_password   = var.admin_password
}
