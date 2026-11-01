resource "mtncloud_instance_deploy" "web" {
  instance_id   = mtncloud_instance.web.id
  deployment_id = mtncloud_deployment.web.id
  version_id    = mtncloud_deployment_version.v2.id

  # Deploy again when the version's files change.
  triggers = {
    index = mtncloud_deployment_file.index.content_sha256
  }
}
