data "mtncloud_git_integration" "tf_example_git_integration" {
  name = "MTNCloudAutomation"
}

resource "mtncloud_shell_script_task" "tfexample_shell_local" {
  name           = "tfexample_shell_local"
  code           = "tfexample_shell_local"
  labels         = ["demo", "terraform"]
  result_type    = "json"
  source_type    = "repository"
  script_path    = "Shell/preinstallcheck.sh"
  version_ref    = "main"
  repository_id  = data.mtncloud_git_integration.tf_example_git_integration.repository_ids["mtncloud-automation-examples"]
  execute_target = "resource"
}