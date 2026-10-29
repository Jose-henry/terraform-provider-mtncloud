resource "mtncloud_load_balancer_listener" "http" {
  network_id       = mtncloud_network.app.id
  load_balancer_id = mtncloud_load_balancer.web.id
  protocol         = "HTTP"
  port             = 80
  default_pool_id  = mtncloud_load_balancer_pool.web.id
}

# Requests for /api go to the API pool; everything else to the listener's default pool.
resource "mtncloud_load_balancer_l7_policy" "api" {
  network_id       = mtncloud_network.app.id
  load_balancer_id = mtncloud_load_balancer.web.id
  listener_id      = mtncloud_load_balancer_listener.http.id
  name             = "api"
  position         = 1
  action           = "REDIRECT_TO_POOL"
  redirect_pool_id = mtncloud_load_balancer_pool.api.id

  rules = [
    { type = "PATH", compare_type = "STARTS_WITH", value = "/api" },
  ]
}

# The old host name is sent to the new one.
resource "mtncloud_load_balancer_l7_policy" "moved" {
  network_id         = mtncloud_network.app.id
  load_balancer_id   = mtncloud_load_balancer.web.id
  listener_id        = mtncloud_load_balancer_listener.http.id
  position           = 2
  action             = "REDIRECT_PREFIX"
  redirect_prefix    = "https://www.example.com"
  redirect_http_code = 301

  rules = [
    { type = "HOST_NAME", compare_type = "EQUAL_TO", value = "old.example.com" },
  ]
}
