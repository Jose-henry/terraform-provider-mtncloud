resource "mtncloud_environment" "tfdemo" {
  active      = true
  code        = "tfdemo"
  description = "Terraform provider demo environment"
  name        = "TFDemo"
}