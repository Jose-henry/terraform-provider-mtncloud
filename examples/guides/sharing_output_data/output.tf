terraform {
  required_providers {
    mtncloud = {
      source  = "mtn/mtncloud"
      version = "0.10.0"
    }
  }
}

provider "mtncloud" {
  url      = var.mtncloud_url
  username = var.mtncloud_username
  password = var.mtncloud_password
}

resource "local_file" "foo" {
  content  = "foo!1"
  filename = "${path.module}/foo.bar"
}

resource "local_file" "foo2" {
  content  = "foo!2"
  filename = "${path.module}/foo2.bar"
}

output "file_md5_hash" {
  value = local_file.foo.content_md5
}

resource "mtncloud_cypher_secret" "foo_hash_cypher_secret" {
  key   = "exampleworkspace/foo_hash"
  value = local_file.foo.content_md5
}

locals {
  file_hashes = [local_file.foo.content_md5, local_file.foo2.content_md5]
}

resource "mtncloud_cypher_secret" "foo_hashes_cypher_secret" {
  key   = "exampleworkspace/foo_hashes"
  value = jsonencode({ "dataoutput" : local.file_hashes })
}