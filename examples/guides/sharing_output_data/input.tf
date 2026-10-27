terraform {
  required_providers {
    mtncloud = {
      source  = "jose-henry/mtncloud"
      version = "0.10.0"
    }
  }
}

provider "mtncloud" {
  url      = var.mtncloud_url
  username = var.mtncloud_username
  password = var.mtncloud_password
}

data "mtncloud_cypher_secret" "shared_test" {
  key = "exampleworkspace/foo_hash"
}

data "mtncloud_cypher_secret" "shared_hash_test" {
  key = "exampleworkspace/foo_hashes"
}

locals {
  hashes = jsondecode(data.mtncloud_cypher_secret.shared_hash_test.value)
}

output "test" {
  value = data.mtncloud_cypher_secret.shared_test.value
}

output "hash_test" {
  value = local.hashes.dataoutput
}