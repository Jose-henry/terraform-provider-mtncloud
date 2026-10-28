resource "mtncloud_conditional_workflow_task" "by_env" {
  name          = "patch-by-environment"
  condition     = "return instance.instanceContext == 'production'"
  if_workflow   = "patch-with-approval"
  else_workflow = "patch-now"
}
