data "mtncloud_workflow" "example_workflow" {
  name = "Deploy app"
}

data "mtncloud_instance" "web" {
  name = "web-1"
}

resource "mtncloud_workflow_job" "date_and_time" {
  name                    = "Deploy app (once)"
  enabled                 = true
  labels                  = ["demo"]
  workflow_id             = data.mtncloud_workflow.example_workflow.id
  schedule_mode           = "date_and_time"
  scheduled_date_and_time = "2027-01-30T06:00:00Z"
  context_type            = "instance"
  instance_ids            = [data.mtncloud_instance.web.id]
  custom_options = {
    "environment" = "test"
  }
}
