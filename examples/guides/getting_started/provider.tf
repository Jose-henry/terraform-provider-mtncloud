terraform {
  required_providers {
    mtncloud = {
      source  = "jose-henry/mtncloud"
      version = "~> 0.1"
    }
  }
}

# Credentials come from the environment (see below), so none are written here.
provider "mtncloud" {
  url = "https://console.cloud.mtn.ng"
}
