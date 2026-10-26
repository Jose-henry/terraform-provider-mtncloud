terraform {
  required_providers {
    mtncloud = {
      source  = "mtn/mtncloud"
      version = "0.12.0"
    }
  }
}

provider "mtncloud" {
  url      = var.mtncloud_url
  username = var.mtncloud_username
  password = var.mtncloud_password
}