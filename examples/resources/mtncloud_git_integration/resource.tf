data "mtncloud_key_pair" "tf_example_key_pair" {
  name = "mtncloudgit"
}

resource "mtncloud_git_integration" "tf_example_git_integration" {
  name               = "tftest"
  enabled            = true
  url                = "https://github.com/gomtncloud/tfdemo.git"
  default_branch     = "main"
  key_pair_id        = data.mtncloud_key_pair.tf_example_key_pair.id
  enable_git_caching = true
}