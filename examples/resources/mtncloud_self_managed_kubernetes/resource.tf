resource "mtncloud_self_managed_kubernetes" "k8s" {
  name                    = "apps-k8s"
  project                 = "web-prod"
  master_plan             = "G2S4"
  master_root_volume_size = 50
  master_networks         = ["app-net"]
  master_security_groups  = ["web"]
  master_count            = 3
  worker_plan             = "G4S16"
  worker_root_volume_size = 100
  worker_networks         = ["app-net"]
  worker_security_groups  = ["web"]
  worker_count            = 3
  cni                     = "calico"
}
