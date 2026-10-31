variable "db_password" {
  type      = string
  sensitive = true
}

# A database VM made by a catalog order: the order owns it, these settings manage it day to day.
resource "mtncloud_mysql" "db" {
  name              = "db-1"
  project           = "web-prod"
  plan              = "G2S4"
  root_volume_size  = 40
  networks          = ["app-net"]
  version           = "8.4"
  database_name     = "shop"
  database_user     = "shop"
  database_password = var.db_password
}

resource "mtncloud_instance_settings" "db" {
  instance_id     = mtncloud_mysql.db.instance_ids[0]
  plan            = "G4S8" # resizes in place, and restarts the VM
  security_groups = ["web"]
  labels          = ["shop", "database"]
  power_state     = "running"
}
