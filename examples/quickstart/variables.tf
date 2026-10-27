variable "mtncloud_url" {
  description = "The MTN Cloud console URL."
  type        = string
  default     = "https://console.cloud.mtn.ng"
}

variable "mtncloud_access_token" {
  description = "MTN Cloud API access token (User Settings > API Access)."
  type        = string
  sensitive   = true
}
