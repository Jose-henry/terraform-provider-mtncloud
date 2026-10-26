resource "mtncloud_max_cores_policy" "tf_example_max_cores_policy_user" {
  name        = "tf_example_max_cores_policy_user"
  description = "Terraform example MTN Cloud max cores policy"
  enabled     = true
  max_cores   = 35
  scope       = "user"
  user_id     = 1
}