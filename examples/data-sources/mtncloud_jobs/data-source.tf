data "mtncloud_jobs" "all" {}

output "jobs" {
  value = { for x in data.mtncloud_jobs.all.jobs : x.id => { name = x.name, last_result = x.last_result } }
}
