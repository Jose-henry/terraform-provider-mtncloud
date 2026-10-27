# By name (matched exactly); `id = 123` works too.
data "mtncloud_form" "example" {
  name = "Request a VM"
}

output "form_code" {
  value = data.mtncloud_form.example.code
}
