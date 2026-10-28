resource "mtncloud_network" "app" {
  name    = "app-net"
  project = "web-prod"
  cidr    = "10.20.1.0/24"
  gateway = "10.20.1.1"
}

resource "mtncloud_security_group" "web" {
  name    = "web"
  project = "web-prod"

  rules = [
    { direction = "ingress", protocol = "tcp", port_range = "443", cidr = "0.0.0.0/0" },
  ]
}

resource "mtncloud_instance" "web" {
  name     = "web-1" # leave out to use your tenant's naming template
  group    = "MTNNG_CLOUD_AZ_1"
  project  = "web-prod"
  image    = "MTN Ubuntu Server 24.04.3LTS"
  plan     = "G2S4"
  networks = [mtncloud_network.app.id]

  # The project's default security group is always applied as well.
  security_groups = [mtncloud_security_group.web.name]

  # A public IP, once the VM runs. The network must be on an MTN Router with an external network.
  floating_ip_pool = "public-network-02"

  root_volume_size = 40
  data_volume {
    size      = 100
    datastore = "__DEFAULT__"
  }

  # Add a VM above 75% CPU, remove one below 30%, between 1 and 3 VMs.
  scale = {
    min_count = 1
    max_count = 3
    cpu       = { min = 30, max = 75 }
  }

  # Power the VM on or off in place ("running", "stopped" or "suspended").
  power_state = "running"
  # Change this to any new value to reboot the VM on the next apply.
  restart_trigger = "2026-10-28"

  environment = "Production"
  labels      = ["web"]
  tags = {
    team = "payments"
  }
}
