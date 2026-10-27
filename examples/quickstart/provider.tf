terraform {
  required_providers {
    mtncloud = {
      source = "jose-henry/mtncloud"
    }
  }
}

# Authenticate with an access token, or with username + password (+ tenant subdomain).
# Every value can also come from MTNCLOUD_API_* environment variables instead.
provider "mtncloud" {
  url          = var.mtncloud_url
  access_token = var.mtncloud_access_token
  secure       = true
}
