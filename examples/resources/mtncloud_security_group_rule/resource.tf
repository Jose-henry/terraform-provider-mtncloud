# A group whose rules are managed one by one: leave "rules" unset on it.
resource "mtncloud_security_group" "app" {
  name    = "app"
  project = "web-prod"
}

resource "mtncloud_security_group_rule" "http" {
  security_group_id = mtncloud_security_group.app.id
  direction         = "ingress"
  protocol          = "tcp"
  port_range        = "8000-8080"
  cidr              = "10.0.0.0/24"
  description       = "app ports"
}
