resource "mtncloud_cypher_password" "db_admin" {
  name   = "db-admin"
  length = 24
}

# Pass it to something that needs it, for example a catalog order's admin password.
output "db_admin_password" {
  value     = mtncloud_cypher_password.db_admin.value
  sensitive = true
}
