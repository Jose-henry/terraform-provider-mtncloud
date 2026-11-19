data "mtncloud_managed_kubernetes_cluster" "k8s" {
  name = "k8s-prod"
}

# Every worker of the Kubernetes cluster with an address on the network joins the pool, and the
# pool follows the cluster as workers are added or removed. 30080 is a NodePort on the workers.
resource "mtncloud_load_balancer_cluster_member" "workers" {
  network_id       = mtncloud_network.app.id
  load_balancer_id = mtncloud_load_balancer.web.id
  pool_id          = mtncloud_load_balancer_pool.web.id
  cluster_id       = data.mtncloud_managed_kubernetes_cluster.k8s.id
  role             = "kube-worker"
  port             = 30080
}

output "worker_members" {
  value = mtncloud_load_balancer_cluster_member.workers.members[*].address
}

# A Docker cluster's hosts are role "docker-worker". 8080 is the port the containers publish on the hosts.
data "mtncloud_docker_cluster" "apps" {
  name = "apps"
}

resource "mtncloud_load_balancer_cluster_member" "docker_hosts" {
  network_id       = mtncloud_network.app.id
  load_balancer_id = mtncloud_load_balancer.web.id
  pool_id          = mtncloud_load_balancer_pool.web.id
  cluster_id       = data.mtncloud_docker_cluster.apps.id
  role             = "docker-worker"
  port             = 8080
}
