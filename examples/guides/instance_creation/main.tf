resource "mtncloud_vsphere_instance" "name" {
  description   = "Terraform instance example"
  cloud_id      = data.mtncloud_cloud.vsphere.id
  group_id      = data.mtncloud_group.all.id
  type          = "centos"
  layout        = "centos"
  plan          = "1 CPU, 4GB Memory"
  environment   = "dev"
  resource_pool = "democluster"
  labels        = ["demo", "terraform"]

  interfaces {
    network = "VM Network"
  }

  tags = {
    name = "tfdemo"
  }

  evar {
    name   = "application"
    value  = "demo"
    export = true
    masked = true
  }
}