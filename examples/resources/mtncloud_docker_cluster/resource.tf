# Two Docker hosts for build jobs.
resource "mtncloud_docker_cluster" "builds" {
  name       = "builds"
  project    = "web-prod"
  host_count = 2

  host = {
    plan            = "G2S4"
    data_volume     = [{ size = 100 }]
    networks        = ["app-net"]
    security_groups = ["builds"]
  }
}
