# "web-prod" is the project you created in the console.

# A private network for the servers.
resource "mtncloud_network" "app" {
  name    = "app-net"
  project = "web-prod"
  cidr    = "10.20.1.0/24"
  gateway = "10.20.1.1"
}

# Connects the network to the internet, so the server can have a public IP.
resource "mtncloud_router" "edge" {
  name             = "edge"
  project          = "web-prod"
  networks         = [mtncloud_network.app.id]
  external_network = "public-network-02"
}

# Lets in HTTPS from anywhere, and SSH from your own address only.
resource "mtncloud_security_group" "web" {
  name    = "web"
  project = "web-prod"

  rules = [
    { direction = "ingress", protocol = "tcp", port_range = "443", cidr = "0.0.0.0/0" },
    { direction = "ingress", protocol = "tcp", port_range = "22", cidr = "203.0.113.10/32" },
  ]
}

# An Ubuntu server. You log in with the Linux settings from your User Settings.
resource "mtncloud_instance" "web" {
  name             = "web-1"
  group            = "MTNNG_CLOUD_AZ_1"
  project          = "web-prod"
  image            = "MTN Ubuntu Server 24.04.3LTS"
  plan             = "G2S4"
  networks         = [mtncloud_network.app.id]
  security_groups  = [mtncloud_security_group.web.name]
  floating_ip_pool = "public-network-02"

  # The public IP needs the router first.
  depends_on = [mtncloud_router.edge]
}

output "web_public_ip" {
  value = mtncloud_instance.web.public_ip
}
