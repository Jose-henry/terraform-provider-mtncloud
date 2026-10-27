data "mtncloud_workflows" "all" {}

output "workflows" {
  value = { for x in data.mtncloud_workflows.all.workflows : x.id => { name = x.name, name = x.name } }
}
