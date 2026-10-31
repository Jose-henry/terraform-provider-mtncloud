# Adopts the tenant's MOS keys, or orders them if the tenant has none.
resource "mtncloud_mos_access" "keys" {}

# To rotate the keys later, for the whole tenant, change the trigger:
#
# resource "mtncloud_mos_access" "keys" {
#   rotate_trigger = "2026-11"
# }

# Buckets made in the same apply wait for the keys.
resource "mtncloud_mos_bucket" "logs" {
  name       = "acme-logs"
  depends_on = [mtncloud_mos_access.keys]
}
