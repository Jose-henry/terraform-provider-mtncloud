# MTN's cluster layouts, for mtncloud_cluster's layout.
data "mtncloud_cluster_layouts" "all" {}

output "layouts" {
  value = data.mtncloud_cluster_layouts.all.cluster_layouts[*].name
}
