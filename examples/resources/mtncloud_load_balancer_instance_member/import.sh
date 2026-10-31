terraform import mtncloud_load_balancer_instance_member.app <network_id>/<load_balancer_id>/<pool_id>/<instance_id>
# A pinned node: the instance's ID, then @ and the node's address.
terraform import mtncloud_load_balancer_instance_member.admin <network_id>/<load_balancer_id>/<pool_id>/<instance_id>@<address>
