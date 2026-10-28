resource "mtncloud_puppet_agent_install_task" "agent" {
  name           = "puppet-agent"
  puppet_master  = "puppet-prod"
  puppet_version = "7"
  environment    = "production"
}
