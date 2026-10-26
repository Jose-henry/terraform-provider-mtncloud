data "mtncloud_integration" "servicenow_prod" {
  name = "SNOW Production"
}

data "mtncloud_servicenow_workflow" "mtncloud_example" {
  name           = "Knowledge - Retire Knowledge"
  integration_id = data.mtncloud_integration.servicenow_prod.id
}

resource "mtncloud_delete_approval_policy" "tf_example_delete_approval_policy_global" {
  name           = "tf_example_delete_approval_policy_global"
  description    = "terraform example global delete approval policy"
  enabled        = true
  integration_id = data.mtncloud_integration.servicenow_prod.id
  workflow_id    = data.mtncloud_servicenow_workflow.mtncloud_example.id
  scope          = "global"
}