variable "db_password" {
  type      = string
  sensitive = true
}

resource "mtncloud_wordpress" "this" {
  name              = "blog-1"
  project           = "web-prod"
  plan              = "G2S4"
  root_volume_size  = 40
  networks          = ["app-net"]
  security_groups   = ["web"]
  floating_ip_pool  = "public-network-01"
  web_server        = "Nginx"
  database_name     = "wordpress"
  database_user     = "wordpress"
  database_password = var.db_password
  php_upload_max    = "64M"
}
