# By name (matched exactly); `id = 123` works too.
data "mtncloud_docker_cluster" "builds" {
  name = "builds"
}

output "host_ids" {
  value = data.mtncloud_docker_cluster.builds.node_ids
}
