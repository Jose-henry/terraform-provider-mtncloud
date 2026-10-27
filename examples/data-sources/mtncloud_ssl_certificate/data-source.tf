# By name (matched exactly); `id = 123` works too.
data "mtncloud_ssl_certificate" "example" {
  name = "www-example"
}

output "ssl_certificate_domain_name" {
  value = data.mtncloud_ssl_certificate.example.domain_name
}
