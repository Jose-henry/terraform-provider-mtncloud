# Only those whose name matches; leave name_regex out to list them all.
data "mtncloud_apps" "all" {
  name_regex = "^web-"
}

output "apps" {
  value = { for x in data.mtncloud_apps.all.apps : x.id => { name = x.name, name = x.name } }
}
