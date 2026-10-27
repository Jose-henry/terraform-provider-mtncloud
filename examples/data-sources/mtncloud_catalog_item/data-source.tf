# One service from the catalog, with the inputs its order form asks for.
data "mtncloud_catalog_item" "project" {
  name = "Create Project"
}

output "required_inputs" {
  value = [for i in data.mtncloud_catalog_item.project.inputs : i.name if i.required]
}
