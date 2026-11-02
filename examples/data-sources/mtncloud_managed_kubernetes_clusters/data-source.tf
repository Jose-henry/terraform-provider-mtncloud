# Every Kubernetes cluster that is up, by name.
data "mtncloud_managed_kubernetes_clusters" "up" {
  status = "ok"
}

output "clusters" {
  value = { for c in data.mtncloud_managed_kubernetes_clusters.up.managed_kubernetes_clusters : c.name => c.layout }
}
