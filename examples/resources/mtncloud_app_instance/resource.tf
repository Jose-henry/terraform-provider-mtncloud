resource "mtncloud_self_managed_kubernetes" "k8s" {
  name                    = "apps-k8s"
  project                 = "web-prod"
  master_plan             = "G2S4"
  master_root_volume_size = 50
  master_networks         = ["app-net"]
  master_security_groups  = ["web"]
  worker_plan             = "G4S16"
  worker_root_volume_size = 100
  worker_networks         = ["app-net"]
  worker_security_groups  = ["web"]
  worker_count            = 3
}

data "mtncloud_network" "app" {
  name    = "app-net"
  project = "web-prod"
}

# A monitoring VM this configuration made, shown in the cluster's app.
resource "mtncloud_instance" "monitor" {
  name             = "k8s-monitor"
  project          = "web-prod"
  image            = "MTN Ubuntu Server 24.04.3LTS"
  plan             = "G2S4"
  root_volume_size = 40
  networks         = [data.mtncloud_network.app.id]
}

resource "mtncloud_app_instance" "monitor" {
  app_id      = mtncloud_self_managed_kubernetes.k8s.app_id
  instance_id = mtncloud_instance.monitor.id
  tier        = "Monitoring"
}
