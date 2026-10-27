data "mtncloud_clusters" "all" {}

output "clusters" {
  value = { for x in data.mtncloud_clusters.all.clusters : x.id => { name = x.name, name = x.name } }
}
