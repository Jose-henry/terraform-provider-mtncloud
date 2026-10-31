data "mtncloud_workflow" "example_workflow" {
  name = "Deploy app"
}

# Runs on every VM labelled "demo" when started by hand.
resource "mtncloud_workflow_job" "manual" {
  name           = "Deploy app (manual)"
  enabled        = true
  labels         = ["demo"]
  workflow_id    = data.mtncloud_workflow.example_workflow.id
  schedule_mode  = "manual"
  context_type   = "instance-label"
  instance_label = "demo"
  custom_options = {
    "environment" = "test"
  }
}
