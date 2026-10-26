resource "mtncloud_wiki_page" "tfexample_wiki_page" {
  name     = "tfexample_wiki_page"
  category = "mtncloud-terraform"
  content  = file("${path.module}/terraform-wiki.md")
}