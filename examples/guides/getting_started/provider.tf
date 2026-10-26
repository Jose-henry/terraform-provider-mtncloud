terraform {
  required_providers {
    mtncloud = {
      source  = "mtn/mtncloud"
      version = "0.12.0"
    }
  }
}

provider "mtncloud" {
  url      = "https://console.cloud.mtn.ng"
  username = "administrator"
  password = "password"
}