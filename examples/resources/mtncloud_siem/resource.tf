variable "admin_password" {
  type      = string
  sensitive = true
}

resource "mtncloud_siem" "this" {
  name             = "siem-1"
  project          = "web-prod"
  plan             = "G2S4"
  root_volume_size = 100
  networks         = ["app-net"]
  security_groups  = ["web"]
  version          = "4.14.6"
  archive_events   = "No"
  admin_password   = var.admin_password
}
