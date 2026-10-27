# An MTN Network in a project. DNS defaults to MTN's resolvers; DHCP and
# static-address override are on by default.
resource "mtncloud_network" "app" {
  name        = "app-net"
  description = "Application tier"
  project     = "web-prod"
  cidr        = "10.20.1.0/24"
  gateway     = "10.20.1.1"
}

# With IPv6 as well, and your own DNS servers.
resource "mtncloud_network" "dual_stack" {
  name          = "edge-net"
  project       = "web-prod"
  cidr          = "10.20.2.0/24"
  gateway       = "10.20.2.1"
  dns_primary   = "102.89.90.112"
  dns_secondary = "120.91.123.54"

  ipv6 = {
    cidr    = "2001:db8:2::/64"
    gateway = "2001:db8:2::1"
  }
}

# The subnet an MTN Router attaches to.
output "app_subnet_id" {
  value = mtncloud_network.app.subnet_id
}
