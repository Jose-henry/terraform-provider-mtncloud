resource "mtncloud_deployment_file" "index" {
  deployment_id = mtncloud_deployment.web.id
  version_id    = mtncloud_deployment_version.v2.id
  name          = "index.html"
  content       = "<h1>Hello from MTN Cloud</h1>"
}

# A folder tree goes up as one archive, unpacked on the instance.
resource "mtncloud_deployment_file" "site" {
  deployment_id = mtncloud_deployment.web.id
  version_id    = mtncloud_deployment_version.v2.id
  name          = "site.tar.gz"
  source        = "${path.module}/build/site.tar.gz"
}
