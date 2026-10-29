# A pool has at most one health monitor, so it is imported by its pool.
terraform import mtncloud_load_balancer_health_monitor.web <network_id>/<load_balancer_id>/<pool_id>
