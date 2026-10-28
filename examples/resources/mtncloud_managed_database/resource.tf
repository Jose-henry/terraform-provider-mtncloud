variable "admin_password" {
  type      = string
  sensitive = true
}

resource "mtncloud_managed_database" "this" {
  name               = "orders-db"
  project            = "web-prod"
  plan               = "G2S4"
  root_volume_size   = 40
  networks           = ["app-net"]
  security_groups    = ["web"]
  engine             = "postgres"
  postgresql_version = "17"
  node_count         = 3
  admin_username     = "admin"
  admin_password     = var.admin_password
}
