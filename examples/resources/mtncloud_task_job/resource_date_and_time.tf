data "mtncloud_task" "example_task" {
  name = "Deploy app"
}

data "mtncloud_instance" "web" {
  name = "web-1"
}

resource "mtncloud_task_job" "date_and_time" {
  name                    = "Deploy app (once)"
  enabled                 = true
  labels                  = ["demo"]
  task_id                 = data.mtncloud_task.example_task.id
  schedule_mode           = "date_and_time"
  scheduled_date_and_time = "2027-01-30T06:00:00Z"
  context_type            = "instance"
  instance_ids            = [data.mtncloud_instance.web.id]
}
