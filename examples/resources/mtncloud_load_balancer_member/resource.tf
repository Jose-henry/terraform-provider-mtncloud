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

# One member per server: here, each web instance's private address.
resource "mtncloud_load_balancer_member" "web" {
  for_each = toset(["10.0.0.11", "10.0.0.12"])

  network_id       = mtncloud_network.app.id
  load_balancer_id = mtncloud_load_balancer.web.id
  pool_id          = mtncloud_load_balancer_pool.web.id
  address          = each.value
  port             = 8080
}
