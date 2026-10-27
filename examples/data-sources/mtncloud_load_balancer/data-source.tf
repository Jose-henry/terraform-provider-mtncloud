# By name (matched exactly); `id = 123` works too.
data "mtncloud_load_balancer" "example" {
  name = "web-lb"
}

output "load_balancer_external_ip" {
  value = data.mtncloud_load_balancer.example.external_ip
}
