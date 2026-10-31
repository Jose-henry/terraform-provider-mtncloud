# Office hours: weekdays 07:00 to 19:30 Lagos time, off at the weekend.
# Days left out (here none) run all day.
resource "mtncloud_power_schedule" "office_hours" {
  name        = "office-hours"
  description = "Dev VMs: weekdays only"
  time_zone   = "Africa/Lagos"

  monday    = { on = "07:00", off = "19:30" }
  tuesday   = { on = "07:00", off = "19:30" }
  wednesday = { on = "07:00", off = "19:30" }
  thursday  = { on = "07:00", off = "19:30" }
  friday    = { on = "07:00", off = "19:30" }
  saturday  = { on = "00:00", off = "00:00" } # off all day
  sunday    = { on = "00:00", off = "00:00" }
}

# Only stops VMs late at night; people start them again when they need them.
resource "mtncloud_power_schedule" "nightly_stop" {
  name      = "nightly-stop"
  time_zone = "Africa/Lagos"
  stop_only = true

  monday    = { on = "00:00", off = "22:00" }
  tuesday   = { on = "00:00", off = "22:00" }
  wednesday = { on = "00:00", off = "22:00" }
  thursday  = { on = "00:00", off = "22:00" }
  friday    = { on = "00:00", off = "22:00" }
}
