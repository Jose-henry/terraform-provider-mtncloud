variable "db_password" {
  type      = string
  sensitive = true
}

resource "mtncloud_postgresql" "this" {
  name              = "pg-1"
  project           = "web-prod"
  plan              = "G2S4"
  root_volume_size  = 40
  networks          = ["app-net"]
  security_groups   = ["web"]
  version           = "17"
  database_name     = "shop"
  database_user     = "shop"
  database_password = var.db_password
}
