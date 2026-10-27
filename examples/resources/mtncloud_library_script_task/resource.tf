# A script template MTN shares with your tenant: its name and ID are shown in the console.
variable "script_template" {
  type = object({ name = string, id = number })
}

resource "mtncloud_library_script_task" "tf_example_library_script_task" {
  name                = "Example Terraform Library Script Task"
  code                = "tf-example-library-script-task"
  labels              = ["demo", "library", "terraform"]
  execute_target      = "resource"
  script_template     = var.script_template.name
  script_template_id  = var.script_template.id
  retryable           = true
  retry_count         = 1
  retry_delay_seconds = 10
  allow_custom_config = true
}