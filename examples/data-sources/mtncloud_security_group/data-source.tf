# Add project when the same name is used in several projects.
data "mtncloud_security_group" "web" {
  name    = "web"
  project = "web-prod"
}

output "web_ingress_rules" {
  value = [for r in data.mtncloud_security_group.web.rules : "${r.protocol}/${coalesce(r.port_range, "all")}" if r.direction == "ingress"]
}
