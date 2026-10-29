resource "mtncloud_load_balancer" "web" {
  network_id = mtncloud_network.app.id
  name       = "web"
}

resource "mtncloud_load_balancer_pool" "web" {
  network_id       = mtncloud_network.app.id
  load_balancer_id = mtncloud_load_balancer.web.id
  protocol         = "HTTP"
  algorithm        = "LEAST_CONNECTIONS"

  # Keep a visitor on one server, by the application's own cookie.
  session_persistence = "APP_COOKIE"
  cookie_name         = "SESSION"
}
