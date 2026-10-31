# By <instance_id>/<schedule_id>. A schedule doesn't record which threshold it was copied from, so the
# first plan after an import sets scale_threshold_id, and applying copies that threshold's values in.
terraform import mtncloud_instance_scale_schedule.web_weekdays 4650/3
