# By the name shown in the console (case ignored), or by code.
data "mtncloud_image" "ubuntu" {
  name = "MTN Ubuntu Server 24.04.3LTS"
}

output "ubuntu_code" {
  value = data.mtncloud_image.ubuntu.code
}
