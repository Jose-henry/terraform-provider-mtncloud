variable "deploy_password" {
  type      = string
  sensitive = true
}

# Makes a Linux user on the VM with MTN's workflow. The VM needs the MTN Cloud agent.
resource "mtncloud_instance_workflow" "deploy_user" {
  workflow     = "Create Linux User Git"
  instance_ids = [mtncloud_instance.web.id]
  inputs = {
    linuxUser  = "deploy"
    makeSudoer = "on"
  }
  # Password fields go here, so they are never shown in a plan.
  secret_inputs = {
    linuxPassword = var.deploy_password
  }
  # Change a value to run it again.
  triggers = {
    round = "1"
  }
}

output "deploy_user_status" {
  value = mtncloud_instance_workflow.deploy_user.status
}
