# Registries a Kubernetes cluster can pull images from.
data "mtncloud_registries" "all" {}

output "registries" {
  value = data.mtncloud_registries.all.registries[*].name
}
