data "mtncloud_instance" "dev" {
  name = "dev-1"
}

resource "mtncloud_power_schedule" "office_hours" {
  name      = "office-hours"
  time_zone = "Africa/Lagos"
  saturday  = { on = "00:00", off = "00:00" }
  sunday    = { on = "00:00", off = "00:00" }
}

resource "mtncloud_instance_power_schedule" "dev" {
  instance_id       = data.mtncloud_instance.dev.id
  power_schedule_id = mtncloud_power_schedule.office_hours.id
}
