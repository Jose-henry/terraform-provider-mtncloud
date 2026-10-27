# The key must already exist; its value is stored in state, marked sensitive.
data "mtncloud_cypher" "db" {
  key = "secret/db-credentials"
}

# An object stored on the secret/ mount: read one entry.
output "db_user" {
  value     = data.mtncloud_cypher.db.values["user"]
  sensitive = true
}
