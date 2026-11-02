# MTN's cluster layouts, which the cluster resources build from.
data "mtncloud_cluster_layouts" "all" {}

output "layouts" {
  value = data.mtncloud_cluster_layouts.all.cluster_layouts[*].name
}
