package compute

import (
	"testing"

	"github.com/mtn/terraform-provider-mtncloud/internal/client"
)

func TestParseScaleScheduleID(t *testing.T) {
	for _, c := range []struct {
		in          string
		inst, sched int64
		ok          bool
	}{
		{"4650/3", 4650, 3, true},
		{"4650", 0, 0, false},
		{"4650/", 0, 0, false},
		{"a/3", 0, 0, false},
		{"0/3", 0, 0, false},
		{"4650/3/1", 0, 0, false},
	} {
		inst, sched, err := parseScaleScheduleID(c.in)
		if (err == nil) != c.ok || inst != c.inst || sched != c.sched {
			t.Errorf("%q: %d, %d, %v", c.in, inst, sched, err)
		}
		if c.ok && scaleScheduleID(inst, sched) != c.in {
			t.Errorf("%q doesn't round-trip", c.in)
		}
	}
}

// MTN Cloud numbers days 1 (Sunday) to 7 (Saturday), live 2026-10-31.
func TestWeekdayNumbers(t *testing.T) {
	for n, name := range map[int64]string{1: "sunday", 2: "monday", 6: "friday", 7: "saturday"} {
		if dayNumber(name) != n || dayName(n) != name {
			t.Errorf("%s: %d, %q", name, dayNumber(name), dayName(n))
		}
	}
	if dayNumber("Monday") != 2 {
		t.Error("day names are matched without case")
	}
	if dayNumber("someday") != 0 || dayName(0) != "" || dayName(8) != "" {
		t.Error("unknown days")
	}
}

func TestSameWhen(t *testing.T) {
	weekly := client.ScaleSchedule{Type: "dayOfWeek", TimeZone: "Africa/Lagos", StartDay: 2, EndDay: 6, StartTime: "08:00", EndTime: "18:00"}
	want := client.ScaleScheduleSpec{Type: "dayOfWeek", TimeZone: "africa/lagos", StartDay: 2, EndDay: 6, StartTime: "08:00", EndTime: "18:00"}
	if !sameWhen(weekly, want) {
		t.Error("same weekly window")
	}
	for name, change := range map[string]func(*client.ScaleScheduleSpec){
		"zone":  func(s *client.ScaleScheduleSpec) { s.TimeZone = "UTC" },
		"day":   func(s *client.ScaleScheduleSpec) { s.EndDay = 7 },
		"time":  func(s *client.ScaleScheduleSpec) { s.EndTime = "19:00" },
		"dated": func(s *client.ScaleScheduleSpec) { s.Type = "exact" },
	} {
		w := want
		change(&w)
		if sameWhen(weekly, w) {
			t.Errorf("another %s matched", name)
		}
	}
	dated := client.ScaleSchedule{Type: "exact", StartDate: "2026-12-24T00:00:00Z", EndDate: "2026-12-27T00:00:00Z"}
	if !sameWhen(dated, client.ScaleScheduleSpec{Type: "exact", StartDate: "2026-12-24T01:00:00+01:00", EndDate: "2026-12-27T00:00:00Z"}) {
		t.Error("the same moments, spelled differently")
	}
	if sameWhen(dated, client.ScaleScheduleSpec{Type: "exact", StartDate: "2026-12-25T00:00:00Z", EndDate: "2026-12-27T00:00:00Z"}) {
		t.Error("another start date matched")
	}
}

func TestSameThreshold(t *testing.T) {
	base := client.Threshold{AutoUp: true, MinCount: 1, MaxCount: 3, Increment: 1, CPU: &client.Range{Min: 10, Max: 80}}
	if !sameThreshold(base, base) {
		t.Error("same values")
	}
	for name, change := range map[string]func(*client.Threshold){
		"auto down": func(t *client.Threshold) { t.AutoDown = true },
		"max count": func(t *client.Threshold) { t.MaxCount = 4 },
		"increment": func(t *client.Threshold) { t.Increment = 2 },
		"cpu":       func(t *client.Threshold) { t.CPU = &client.Range{Min: 10, Max: 70} },
		"cpu off":   func(t *client.Threshold) { t.CPU = nil },
		"memory":    func(t *client.Threshold) { t.Memory = &client.Range{Max: 90} },
	} {
		o := base
		change(&o)
		if sameThreshold(base, o) {
			t.Errorf("a different %s matched", name)
		}
	}
}
