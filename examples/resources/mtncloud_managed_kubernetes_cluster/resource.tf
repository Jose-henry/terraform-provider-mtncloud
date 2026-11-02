# A Kubernetes cluster with three masters and three workers that can scale to six, and bigger
# workers than masters. Every node gets a fixed 100 GB root disk plus the data disks below.
resource "mtncloud_managed_kubernetes_cluster" "apps" {
  name        = "apps"
  layout      = "ha3"
  project     = "web-prod"
  description = "Storefront workloads"
  labels      = ["storefront"]

  kubernetes_version = "1.35.3"
  pod_cidr           = "10.244.0.0/16"
  service_cidr       = "10.96.0.0/12"

  master = {
    plan            = "G2S4"
    data_volume     = [{ size = 50 }]
    networks        = ["app-net"]
    security_groups = ["k8s-nodes"] # allows TCP 6443 and 10250 between the nodes
  }

  worker_count = 3
  worker = {
    plan        = "G4S16"
    data_volume = [{ size = 200, datastore = "ENCR_AT_REST" }]
  }

  autoscaling = {
    min_nodes = 3
    max_nodes = 6
  }
}
