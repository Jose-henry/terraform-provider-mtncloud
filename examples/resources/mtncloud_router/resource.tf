resource "mtncloud_network" "app" {
  name    = "app-net"
  project = "web-prod"
  cidr    = "10.20.1.0/24"
  gateway = "10.20.1.1"
}

resource "mtncloud_network" "db" {
  name    = "db-net"
  project = "web-prod"
  cidr    = "10.20.2.0/24"
  gateway = "10.20.2.1"
}

# Connects both networks, and gives them a way out through a public network.
resource "mtncloud_router" "edge" {
  name             = "edge"
  project          = "web-prod"
  networks         = [mtncloud_network.app.id, mtncloud_network.db.id]
  external_network = "public-network-02"
}

output "edge_external_ip" {
  value = mtncloud_router.edge.external_ip
}
