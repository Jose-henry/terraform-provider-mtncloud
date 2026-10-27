# An instance made in the console, by name. Add project if the name is used in several projects.
data "mtncloud_instance" "db" {
  name    = "db-1"
  project = "web-prod"
}

# The same lookup by ID, for example to import it: terraform import mtncloud_instance.db <id>
data "mtncloud_instance" "by_id" {
  id = data.mtncloud_instance.db.id
}

output "db_address" {
  value = data.mtncloud_instance.db.private_ip
}
