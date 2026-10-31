data "mtncloud_task" "example_task" {
  name = "Deploy app"
}

# Runs on every VM labelled "demo" when started by hand.
resource "mtncloud_task_job" "manual" {
  name           = "Deploy app (manual)"
  enabled        = true
  labels         = ["demo"]
  task_id        = data.mtncloud_task.example_task.id
  schedule_mode  = "manual"
  context_type   = "instance-label"
  instance_label = "demo"
}
