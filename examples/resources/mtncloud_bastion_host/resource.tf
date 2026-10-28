variable "admin_password" {
  type      = string
  sensitive = true
}

resource "mtncloud_bastion_host" "this" {
  name             = "bastion-1"
  project          = "web-prod"
  plan             = "G2S4"
  root_volume_size = 40
  networks         = ["app-net"]
  security_groups  = ["web"]
  floating_ip_pool = "public-network-01"
  admin_password   = var.admin_password
}
