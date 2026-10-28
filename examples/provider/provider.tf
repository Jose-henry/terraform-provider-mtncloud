terraform {
  required_providers {
    mtncloud = {
      source  = "jose-henry/mtncloud"
      version = "~> 0.4"
    }
  }
}

provider "mtncloud" {
  url      = var.mtncloud_url
  username = var.mtncloud_username
  password = var.mtncloud_password

  # Optional defaults: resources that leave out project (or group) use these.
  project = var.mtncloud_project
}
