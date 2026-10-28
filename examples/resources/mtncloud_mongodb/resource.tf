variable "admin_password" {
  type      = string
  sensitive = true
}

resource "mtncloud_mongodb" "this" {
  name             = "mongo-1"
  project          = "web-prod"
  plan             = "G2S4"
  root_volume_size = 40
  networks         = ["app-net"]
  security_groups  = ["web"]
  version          = "8.2"
  admin_username   = "admin"
  admin_password   = var.admin_password
}
