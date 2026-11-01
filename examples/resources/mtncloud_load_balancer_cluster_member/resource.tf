data "mtncloud_cluster" "k8s" {
  name = "k8s-prod"
}

# Every worker of the Kubernetes cluster with an address on the network joins the pool, and the
# pool follows the cluster as workers are added or removed. 30080 is a NodePort on the workers.
resource "mtncloud_load_balancer_cluster_member" "workers" {
  network_id       = mtncloud_network.app.id
  load_balancer_id = mtncloud_load_balancer.web.id
  pool_id          = mtncloud_load_balancer_pool.web.id
  cluster_id       = data.mtncloud_cluster.k8s.id
  role             = "kube-worker"
  port             = 30080
}

output "worker_members" {
  value = mtncloud_load_balancer_cluster_member.workers.members[*].address
}
