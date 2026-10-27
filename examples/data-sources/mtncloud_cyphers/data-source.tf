# Only those whose name matches; leave name_regex out to list them all.
data "mtncloud_cyphers" "all" {
  name_regex = "^secret/"
}

output "cyphers" {
  value = { for x in data.mtncloud_cyphers.all.cyphers : x.key => x.expire_date }
}
