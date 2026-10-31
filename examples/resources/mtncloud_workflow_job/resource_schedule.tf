data "mtncloud_workflow" "example_workflow" {
  name = "Deploy app"
}

data "mtncloud_instance" "web" {
  name = "web-1"
}

# The schedule must be enabled: MTN Cloud refuses a job on a disabled one ("Schedule not found").
data "mtncloud_execute_schedule" "example_schedule" {
  name = "Run Daily at 9 AM"
}

resource "mtncloud_workflow_job" "schedule" {
  name                  = "Deploy app (daily)"
  enabled               = true
  labels                = ["demo"]
  workflow_id           = data.mtncloud_workflow.example_workflow.id
  schedule_mode         = "scheduled"
  execution_schedule_id = data.mtncloud_execute_schedule.example_schedule.id
  context_type          = "instance"
  instance_ids          = [data.mtncloud_instance.web.id]
  custom_options = {
    "environment" = "test"
  }
}
