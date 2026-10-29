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

resource "mtncloud_load_balancer_listener" "http" {
  network_id       = mtncloud_network.app.id
  load_balancer_id = mtncloud_load_balancer.web.id
  protocol         = "HTTP"
  port             = 80
  default_pool_id  = mtncloud_load_balancer_pool.web.id

  # Optional: only these sources may connect.
  allowed_cidrs = ["203.0.113.0/24"]
}
