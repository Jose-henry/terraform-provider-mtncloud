# Orders MTN's "Project" catalog item. MTN Cloud names it "web-prod-<tenant>".
resource "mtncloud_project" "web" {
  name        = "web-prod"
  description = "Web tier, production"
}
