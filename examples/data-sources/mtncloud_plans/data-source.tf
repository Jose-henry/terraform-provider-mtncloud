# Sizes the "web-prod" project offers for Ubuntu 24.04 with at least 2 vCPUs and 4 GB of memory.
data "mtncloud_plans" "web" {
  project       = "web-prod"
  image         = "MTN-U24.04LTS"
  min_cores     = 2
  min_memory_gb = 4
}

# Plans come back smallest first, so this is the smallest size that fits.
output "smallest_fit" {
  value = data.mtncloud_plans.web.plans[0].name
}
