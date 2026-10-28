variable "share_password" {
  type      = string
  sensitive = true
}

resource "mtncloud_smb_share" "files" {
  name             = "team-files"
  quota_gb         = 100
  client_ips       = ["10.0.0.0/24"]
  password         = var.share_password
  password_version = 1 # change it to send a new password
}
