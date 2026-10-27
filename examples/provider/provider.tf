terraform {
  required_providers {
    mtncloud = {
      source  = "jose-henry/mtncloud"
      version = "~> 0.3"
    }
  }
}

provider "mtncloud" {
  url      = var.mtncloud_url
  username = var.mtncloud_username
  password = var.mtncloud_password
}