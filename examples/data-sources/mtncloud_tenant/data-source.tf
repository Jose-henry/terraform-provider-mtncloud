# The tenant this login belongs to.
data "mtncloud_tenant" "me" {}

output "tenant" {
  value = {
    id   = data.mtncloud_tenant.me.id
    name = data.mtncloud_tenant.me.name
  }
}
