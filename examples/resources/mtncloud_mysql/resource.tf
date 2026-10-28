variable "db_password" {
  type      = string
  sensitive = true
}

resource "mtncloud_mysql" "this" {
  name              = "db-1"
  project           = "web-prod"
  plan              = "G2S4"
  root_volume_size  = 40
  networks          = ["app-net"]
  security_groups   = ["web"]
  version           = "8.4"
  database_name     = "shop"
  database_user     = "shop"
  database_password = var.db_password
  allowed_cidrs     = ["10.0.0.0/24"]
}
