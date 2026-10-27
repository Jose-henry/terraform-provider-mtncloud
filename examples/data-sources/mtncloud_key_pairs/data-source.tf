data "mtncloud_key_pairs" "all" {}

output "key_pairs" {
  value = { for x in data.mtncloud_key_pairs.all.key_pairs : x.id => { name = x.name, fingerprint = x.fingerprint } }
}
