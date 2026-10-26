data "mtncloud_workflow" "example_workflow" {
  name = "Example Workflow"
}

resource "mtncloud_nested_workflow_task" "tfexample_nested_workflow" {
  name                      = "tfexample_nested_workflow"
  code                      = "tfexample_nested_workflow"
  labels                    = ["demo", "terraform"]
  operational_workflow_id   = data.mtncloud_workflow.example_workflow.id
  operational_workflow_name = data.mtncloud_workflow.example_workflow.name
}