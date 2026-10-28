variable "shop_key" {
  type      = string
  sensitive = true
}

resource "mtncloud_ssl_certificate" "shop" {
  name                = "shop-example-com"
  description         = "Storefront certificate"
  domain_name         = "shop.example.com"
  certificate         = file("${path.module}/shop.crt")
  chain               = file("${path.module}/chain.crt")
  private_key         = var.shop_key
  private_key_version = 1 # change it, with the new key, to send the key again
}
