# A file template MTN shares with your tenant: its name and ID are shown in the console.
variable "file_template" {
  type = object({ name = string, id = number })
}

resource "mtncloud_library_template_task" "tf_example_library_template_task" {
  name                = "Example Terraform Library Template Task"
  code                = "tf-example-library-template-task"
  labels              = ["demo", "library", "terraform"]
  execute_target      = "resource"
  file_template       = var.file_template.name
  file_template_id    = var.file_template.id
  retryable           = true
  retry_count         = 1
  retry_delay_seconds = 10
  allow_custom_config = true
}