variable "admin_password" {
  type      = string
  sensitive = true
}

resource "mtncloud_managed_mq" "this" {
  name             = "events-mq"
  project          = "web-prod"
  plan             = "G2S4"
  root_volume_size = 40
  networks         = ["app-net"]
  security_groups  = ["web"]
  engine           = "rabbit"
  rabbitmq_version = "4.3.2"
  node_count       = 3
  admin_username   = "admin"
  admin_password   = var.admin_password
}
