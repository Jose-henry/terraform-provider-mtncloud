# A Kubernetes cluster with three masters, three workers that can scale to six,
# bigger workers than masters, and a public IP on every node.
resource "mtncloud_cluster" "apps" {
  name        = "apps"
  layout      = "MTN Kubernetes HA (3)"
  project     = "web-prod"
  description = "Storefront workloads"
  labels      = ["storefront"]

  kubernetes_version = "1.35.3"
  pod_cidr           = "10.244.0.0/16"
  service_cidr       = "10.96.0.0/12"
  domain             = "localdomain"

  master = {
    plan             = "G2S4"
    root_volume_size = 50
    data_volume      = [{ size = 50 }]
    networks         = ["app-net"]
    security_groups  = ["default", "k8s-api"]
    floating_ip_pool = "public-network-01"
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

# A single Docker host.
resource "mtncloud_cluster" "builds" {
  name    = "builds"
  type    = "docker"
  project = "web-prod"

  master = {
    plan             = "G2S4"
    root_volume_size = 40
    data_volume      = [{ size = 100 }]
    networks         = ["app-net"]
  }
}
