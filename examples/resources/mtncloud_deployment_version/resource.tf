# Files uploaded to MTN Cloud, with mtncloud_deployment_file.
resource "mtncloud_deployment_version" "v2" {
  deployment_id = mtncloud_deployment.web.id
  version       = "2.0.0"
  type          = "file"
}

# A git repository at a tag.
resource "mtncloud_deployment_version" "from_git" {
  deployment_id = mtncloud_deployment.web.id
  version       = "2.1.0"
  type          = "git"
  git_url       = "https://github.com/example/web-site.git"
  git_ref       = "v2.1.0"
}

# A URL MTN Cloud downloads when it deploys.
resource "mtncloud_deployment_version" "release" {
  deployment_id = mtncloud_deployment.web.id
  version       = "2.2.0"
  type          = "fetch"
  fetch_url     = "https://downloads.example.com/web-site-2.2.0.tar.gz"
}
