# A library threshold for office hours: up to 4 servers when CPU runs hot.
resource "mtncloud_scale_threshold" "office_hours" {
  name                 = "office-hours"
  auto_upscale         = true
  auto_downscale       = true
  min_count            = 1
  max_count            = 4
  enable_cpu_threshold = true
  min_cpu_percentage   = 20
  max_cpu_percentage   = 75
}

# Apply it to the web VM from Monday 08:00 to Friday 18:00, Lagos time.
resource "mtncloud_instance_scale_schedule" "web_weekdays" {
  instance_id        = mtncloud_instance.web.id
  scale_threshold_id = mtncloud_scale_threshold.office_hours.id
  time_zone          = "Africa/Lagos"
  start_day          = "monday"
  start_time         = "08:00"
  end_day            = "friday"
  end_time           = "18:00"

  # Copy a changed threshold in the same apply, by making the schedule again.
  lifecycle {
    replace_triggered_by = [mtncloud_scale_threshold.office_hours]
  }
}

# A one-off window, with the same threshold.
resource "mtncloud_instance_scale_schedule" "web_sale" {
  instance_id        = mtncloud_instance.web.id
  scale_threshold_id = mtncloud_scale_threshold.office_hours.id
  start_date         = "2026-11-27T00:00:00+01:00"
  end_date           = "2026-11-30T23:59:00+01:00"

  lifecycle {
    replace_triggered_by = [mtncloud_scale_threshold.office_hours]
  }
}
