# Public IPs on the instances of one project; leave project out for all of them.
data "mtncloud_floating_ips" "web" {
  project = "web-prod"
}

output "public_ips" {
  value = { for f in data.mtncloud_floating_ips.web.floating_ips : f.ip => f.instance }
}
