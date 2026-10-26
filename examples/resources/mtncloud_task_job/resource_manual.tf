data "mtncloud_task" "example_task" {
  name = "Deploy app"
}

resource "mtncloud_task_job" "tf_example_task_job_manual" {
  name           = "TF Example Task Job Manual"
  enabled        = true
  labels         = ["aws", "demo"]
  task_id        = data.mtncloud_task.example_task.id
  schedule_mode  = "manual"
  context_type   = "instance-label"
  instance_label = "demo"
}