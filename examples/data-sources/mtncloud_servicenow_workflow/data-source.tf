data "mtncloud_integration" "servicenow_prod" {
  name = "SNOW Production"
}

data "mtncloud_servicenow_workflow" "mtncloud_example" {
  name           = "MTN Cloud Approvals"
  integration_id = data.mtncloud_integration.servicenow_prod.id
}