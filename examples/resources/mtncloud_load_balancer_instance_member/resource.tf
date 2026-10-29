# Every address the instance has on the network joins the pool, and the pool follows the
# instance as it scales.
resource "mtncloud_load_balancer_instance_member" "app" {
  network_id       = mtncloud_network.app.id
  load_balancer_id = mtncloud_load_balancer.web.id
  pool_id          = mtncloud_load_balancer_pool.web.id
  instance_id      = mtncloud_instance.app.id
  port             = 8080
}

output "app_members" {
  value = mtncloud_load_balancer_instance_member.app.members[*].address
}
