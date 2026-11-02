data "mtncloud_docker_clusters" "all" {}

output "docker_clusters" {
  value = [for c in data.mtncloud_docker_clusters.all.docker_clusters : c.name]
}
