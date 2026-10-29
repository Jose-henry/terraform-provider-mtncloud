resource "mtncloud_load_balancer" "web" {
  network_id = mtncloud_network.app.id
  name       = "web"
}

resource "mtncloud_load_balancer_pool" "web" {
  network_id       = mtncloud_network.app.id
  load_balancer_id = mtncloud_load_balancer.web.id
  protocol         = "HTTP"
  algorithm        = "ROUND_ROBIN"
}

resource "mtncloud_load_balancer_health_monitor" "web" {
  network_id       = mtncloud_network.app.id
  load_balancer_id = mtncloud_load_balancer.web.id
  pool_id          = mtncloud_load_balancer_pool.web.id
  type             = "HTTP"
  delay            = 5
  timeout          = 3
  max_retries      = 3
  url_path         = "/health"
}
