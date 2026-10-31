terraform {
  required_providers {
    mtncloud = {
      source  = "mtncloud-mcs/mtncloud"
      version = "~> 0.7"
    }
  }
}

# Credentials come from the environment (see below), so none are written here.
provider "mtncloud" {
  url = "https://console.cloud.mtn.ng"
}
