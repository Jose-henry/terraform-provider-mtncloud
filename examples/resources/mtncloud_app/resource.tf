resource "mtncloud_app" "shop" {
  name        = "shop"
  description = "Shop front and its workers"
  labels      = ["shop"]
  environment = "Production"
}

resource "mtncloud_instance" "web" {
  name             = "shop-web-1"
  project          = "web-prod"
  image            = "MTN Ubuntu Server 24.04.3LTS"
  plan             = "G2S4"
  root_volume_size = 40
  networks         = ["app-net"]
}

# The instance joins the app's Web tier. Destroying the app leaves the instance running.
resource "mtncloud_app_instance" "web" {
  app_id      = mtncloud_app.shop.id
  instance_id = mtncloud_instance.web.id
  tier        = "Web"
}
