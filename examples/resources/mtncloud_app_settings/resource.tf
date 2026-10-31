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

# The order owns the app; these settings manage its name, labels and environment day to day.
resource "mtncloud_app_settings" "k8s" {
  app_id      = mtncloud_self_managed_kubernetes.k8s.app_id
  description = "Shop platform cluster"
  labels      = ["shop", "kubernetes"]
  environment = "Production"
}

# Each of the app's VMs (masters and workers) gets the same day-2 settings, one resource per VM.
# A VM added to the app later is covered once the order's instance_ids shows it.
resource "mtncloud_instance_settings" "node" {
  for_each        = toset(mtncloud_self_managed_kubernetes.k8s.instance_ids)
  instance_id     = each.value
  labels          = ["shop", "kubernetes"]
  security_groups = ["web"]
}
