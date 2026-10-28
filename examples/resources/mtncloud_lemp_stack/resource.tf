variable "db_password" {
  type      = string
  sensitive = true
}

resource "mtncloud_lemp_stack" "this" {
  name              = "lemp-1"
  project           = "web-prod"
  plan              = "G2S4"
  root_volume_size  = 40
  networks          = ["app-net"]
  security_groups   = ["web"]
  floating_ip_pool  = "public-network-01"
  web_server_port   = 80
  database_name     = "app"
  database_user     = "app"
  database_password = var.db_password
}
