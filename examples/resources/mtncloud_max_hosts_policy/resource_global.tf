resource "mtncloud_max_hosts_policy" "tf_example_max_hosts_policy_global" {
  name        = "tf_example_max_hosts_policy_global"
  description = "Terraform example MTN Cloud max hosts policy"
  enabled     = true
  max_hosts   = 35
  scope       = "global"
}