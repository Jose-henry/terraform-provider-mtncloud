# By name (matched exactly); `id = 123` works too.
data "mtncloud_managed_kubernetes_cluster" "prod" {
  name = "k8s-prod"
}

output "api_url" {
  value = data.mtncloud_managed_kubernetes_cluster.prod.api_url
}
