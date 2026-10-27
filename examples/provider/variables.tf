variable "mtncloud_url" {
  description = "The MTN Cloud console URL, for example https://console.cloud.mtn.ng"
  type        = string
}

variable "mtncloud_username" {
  description = "The username of the user account used to access MTN Cloud"
  type        = string
}

variable "mtncloud_password" {
  description = "The password of the user account used to access MTN Cloud"
  type        = string
  sensitive   = true
}

variable "mtncloud_project" {
  description = "The project resources go in when they don't name one, for example web-prod"
  type        = string
}
