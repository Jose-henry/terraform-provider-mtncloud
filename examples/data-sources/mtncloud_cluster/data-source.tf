# By name (matched exactly); `id = 123` works too.
data "mtncloud_cluster" "example" {
  name = "k8s-prod"
}

output "cluster_type" {
  value = data.mtncloud_cluster.example.type
}
