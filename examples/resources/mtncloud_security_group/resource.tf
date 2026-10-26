# Rules managed on the group. The two default egress rules (allow all outbound traffic)
# stay in place and are listed in all_rules once MTN Cloud has synced them.
resource "mtncloud_security_group" "web" {
  name        = "web"
  description = "Web servers"
  project     = "web-prod"

  rules = [
    { direction = "ingress", protocol = "tcp", port_range = "443", cidr = "0.0.0.0/0", description = "https" },
    { direction = "ingress", protocol = "tcp", port_range = "22", cidr = "10.0.0.0/24", description = "ssh from admin" },
    { direction = "ingress", protocol = "icmp", cidr = "10.0.0.0/16" },
  ]
}

# Outbound traffic limited to what is listed, once the default egress rules are gone.
# OpenStack adds them at create; MTN Cloud shows them after its sync, and the first apply
# after that removes them. Until then this group still allows all outbound traffic.
resource "mtncloud_security_group" "db" {
  name                  = "db"
  project               = "web-prod"
  remove_default_egress = true

  rules = [
    { direction = "ingress", protocol = "tcp", port_range = "5432", source_security_group_id = mtncloud_security_group.web.id },
    { direction = "egress", protocol = "udp", port_range = "53", cidr = "10.0.0.0/16" },
  ]
}

# Every rule on the group, defaults included.
output "web_rules" {
  value = mtncloud_security_group.web.all_rules
}
