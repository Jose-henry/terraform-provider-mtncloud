resource "mtncloud_letsencrypt_certificate" "shop" {
  instance_id = mtncloud_instance.web.id
  domain_name = "shop.example.com"
  admin_email = "ops@example.com"
}
