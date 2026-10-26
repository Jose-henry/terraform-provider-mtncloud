resource "mtncloud_instance_layout" "tf_example_instance_layout" {
  instance_type_id = mtncloud_instance_type.tf_example_instance_type.id
  name             = "todo_app_frontend"
  labels           = ["demo", "layout", "terraform"]
  version          = "1.0"
  technology       = "vmware"
  node_type_ids = [
    mtncloud_node_type.ubuntu_base.id
  ]
  workflow_id = mtncloud_provisioning_workflow.tfexample_workflow.id
}