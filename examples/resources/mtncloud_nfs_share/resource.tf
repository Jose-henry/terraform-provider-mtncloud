resource "mtncloud_nfs_share" "data" {
  name       = "app-data"
  quota_gb   = 50
  client_ips = ["10.0.0.10", "10.0.0.11"]
}
