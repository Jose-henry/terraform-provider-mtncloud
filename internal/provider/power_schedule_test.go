package provider

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

var weekdayNames = []string{"monday", "tuesday", "wednesday", "thursday", "friday", "saturday", "sunday"}

// powerFake is MTN Cloud's power schedules and instance 4660's schedule. Like the platform (live QA
// 2026-10-31, t114): a PUT keeps fields it isn't sent, days left out of a create are on all day,
// an off time before the on time is refused, and a schedule in use can't be deleted.
type powerFake struct {
	mu        sync.Mutex
	schedules map[int64]map[string]any
	next      int64
	onSched   int64 // instance 4660's schedule, 0 for none
	gone      bool  // instance 4660 deleted
	posts     int
}

func newPowerFake(f *fakeAPI) *powerFake {
	p := &powerFake{schedules: map[int64]map[string]any{}, next: 10}
	f.mu.Lock()
	f.power = p
	f.mu.Unlock()
	return p
}

func (p *powerFake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	p.mu.Lock()
	defer p.mu.Unlock()
	write := func(v any) { b, _ := json.Marshal(v); _, _ = w.Write(b) }
	var in map[string]map[string]any
	if b, _ := io.ReadAll(r.Body); len(b) > 0 {
		_ = json.Unmarshal(b, &in)
	}
	if r.URL.Path == "/api/instances/4660" {
		if p.gone {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if r.Method == http.MethodPut {
			switch v := in["instance"]["powerScheduleType"].(type) {
			case nil:
				if _, sent := in["instance"]["powerScheduleType"]; sent {
					p.onSched = 0
				}
			case float64:
				p.onSched = int64(v)
			}
		}
		var ps any
		if p.onSched > 0 {
			ps = map[string]any{"type": map[string]any{"id": p.onSched, "name": "x"}, "timezone": nil}
		}
		write(map[string]any{"instance": map[string]any{"id": 4660, "powerSchedule": ps}})
		return
	}
	var id int64
	_, _ = fmt.Sscanf(r.URL.Path, "/api/power-schedules/%d", &id)
	refuse := func(body map[string]any) bool {
		for _, d := range weekdayNames {
			on, _ := body[d+"OnTime"].(string)
			off, _ := body[d+"OffTime"].(string)
			if on != "" && off != "" && off < on {
				w.WriteHeader(http.StatusBadRequest)
				write(map[string]any{"success": false, "errors": map[string]any{d + "Off": "Off time cannot be earlier than On time"}})
				return true
			}
		}
		return false
	}
	switch {
	case r.Method == http.MethodGet && id == 0:
		var list []map[string]any
		for _, s := range p.schedules {
			if strings.EqualFold(s["name"].(string), r.URL.Query().Get("name")) {
				list = append(list, s)
			}
		}
		write(map[string]any{"schedules": list})
	case r.Method == http.MethodPost:
		body := in["schedule"]
		if refuse(body) {
			return
		}
		p.posts++
		s := map[string]any{"id": p.next, "visibility": "private", "enabled": true, "scheduleType": "power", "scheduleTimezone": "UTC", "description": nil}
		for _, d := range weekdayNames {
			s[d+"OnTime"], s[d+"OffTime"] = "00:00", "24:00"
		}
		p.apply(s, body)
		p.schedules[p.next] = s
		p.next++
		write(map[string]any{"schedule": s, "success": true})
	default:
		s, ok := p.schedules[id]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		switch r.Method {
		case http.MethodGet:
			write(map[string]any{"schedule": s})
		case http.MethodPut:
			if refuse(in["schedule"]) {
				return
			}
			p.apply(s, in["schedule"])
			write(map[string]any{"schedule": s, "success": true})
		case http.MethodDelete:
			if p.onSched == id {
				w.WriteHeader(http.StatusBadRequest)
				write(map[string]any{"success": false, "msg": "The power schedule is currently in use and could not be deleted."})
				return
			}
			delete(p.schedules, id)
			write(map[string]any{"success": true})
		}
	}
}

// apply copies sent fields, as the platform's PUT does: fields not sent keep their values.
func (p *powerFake) apply(s, body map[string]any) {
	for k, v := range body {
		if k == "description" && v == "" {
			v = nil
		}
		s[k] = v
	}
	hours := 0.0
	for _, d := range weekdayNames {
		on, off := s[d+"OnTime"].(string), s[d+"OffTime"].(string)
		var h1, m1, h2, m2 float64
		_, _ = fmt.Sscanf(on, "%f:%f", &h1, &m1)
		_, _ = fmt.Sscanf(off, "%f:%f", &h2, &m2)
		hours += 24 - ((h2 + m2/60) - (h1 + m1/60))
	}
	s["totalMonthlyHoursSaved"] = hours * 4.35
}

func TestPowerSchedule(t *testing.T) {
	f := newFakeAPI(t)
	p := newPowerFake(f)
	cfg := func(sched, attach string) string {
		return f.providerConfig() + `
resource "mtncloud_power_schedule" "office" {
  name = "office-hours"
` + sched + `}
` + attach
	}
	office := `  description = "weekdays only"
  time_zone   = "Africa/Lagos"
  monday      = { on = "07:00", off = "19:30" }
  tuesday     = { on = "07:00", off = "19:30" }
  saturday    = { on = "00:00", off = "00:00" }
  sunday      = { on = "00:00", off = "00:00" }
`
	attach := `
resource "mtncloud_instance_power_schedule" "web" {
  instance_id       = "4660"
  power_schedule_id = mtncloud_power_schedule.office.id
}
`
	const sched, att = "mtncloud_power_schedule.office", "mtncloud_instance_power_schedule.web"
	empty := resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()}}
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories(),
		Steps: []resource.TestStep{
			// Plan-time checks, before anything is sent.
			{Config: cfg(`  monday = { on = "19:00", off = "07:00" }
`, ""), ExpectError: regexp.MustCompile(`off \(07:00\) is before on`)},
			{Config: cfg(`  monday = { on = "00:00", off = "24:00" }
`, ""), ExpectError: regexp.MustCompile(`runs all day: leave monday out`)},
			{Config: cfg(`  monday = { on = "07:00", off = "07:00" }
`, ""), ExpectError: regexp.MustCompile(`on and off are both 07:00`)},
			{Config: cfg(`  monday = { on = "7:00", off = "19:00" }
`, ""), ExpectError: regexp.MustCompile(`must be a 24-hour time`)},
			// Create, attach, then a clean plan.
			{Config: cfg(office, attach), ConfigPlanChecks: resource.ConfigPlanChecks{PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()}},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(sched, "enabled", "true"),
					resource.TestCheckResourceAttr(sched, "stop_only", "false"),
					resource.TestCheckResourceAttr(sched, "monday.off", "19:30"),
					resource.TestCheckNoResourceAttr(sched, "wednesday"),
					resource.TestCheckResourceAttrSet(sched, "monthly_hours_saved"),
					resource.TestCheckResourceAttrPair(att, "power_schedule_id", sched, "id"),
					resource.TestCheckResourceAttr(att, "id", "4660"),
					func(*terraform.State) error {
						p.mu.Lock()
						defer p.mu.Unlock()
						s := p.schedules[10]
						if s["wednesdayOnTime"] != "00:00" || s["wednesdayOffTime"] != "24:00" || s["sundayOffTime"] != "00:00" || p.onSched != 10 {
							return fmt.Errorf("sent %v, instance on %d", s, p.onSched)
						}
						return nil
					},
				)},
			{Config: cfg(office, attach), ConfigPlanChecks: empty},
			// In place: a day removed goes back to all day (sent, since PUT keeps what it isn't sent),
			// stop only, and the description cleared.
			{Config: cfg(`  time_zone = "Africa/Lagos"
  monday    = { on = "07:00", off = "19:30" }
  stop_only = true
`, attach), ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(sched, plancheck.ResourceActionUpdate)}},
				Check: func(*terraform.State) error {
					p.mu.Lock()
					defer p.mu.Unlock()
					s := p.schedules[10]
					if s["sundayOffTime"] != "24:00" || s["scheduleType"] != "power off" || s["description"] != nil {
						return fmt.Errorf("after update: %v", s)
					}
					return nil
				}},
			// MTN Cloud returning the time zone in another case is no change.
			{PreConfig: func() { p.mu.Lock(); p.schedules[10]["scheduleTimezone"] = "AFRICA/LAGOS"; p.mu.Unlock() }, Config: cfg(`  time_zone = "Africa/Lagos"
  monday    = { on = "07:00", off = "19:30" }
  stop_only = true
`, attach), ConfigPlanChecks: empty},
			// Import both.
			{PreConfig: func() { p.mu.Lock(); p.schedules[10]["scheduleTimezone"] = "Africa/Lagos"; p.mu.Unlock() },
				ResourceName: sched, ImportState: true, ImportStateVerify: true, Config: cfg(`  time_zone = "Africa/Lagos"
  monday    = { on = "07:00", off = "19:30" }
  stop_only = true
`, attach)},
			{ResourceName: att, ImportState: true, ImportStateVerify: true, ImportStateId: "4660"},
			// Taken off the schedule out of band: the plan puts it back.
			{PreConfig: func() { p.mu.Lock(); p.onSched = 0; p.mu.Unlock() }, Config: cfg(`  time_zone = "Africa/Lagos"
  monday    = { on = "07:00", off = "19:30" }
  stop_only = true
`, attach), ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(att, plancheck.ResourceActionCreate)}}},
			// Schedule deleted out of band (after the instance was taken off it): plan recreates it.
			{PreConfig: func() { p.mu.Lock(); p.onSched = 0; delete(p.schedules, 10); p.mu.Unlock() }, Config: cfg(`  time_zone = "Africa/Lagos"
  monday    = { on = "07:00", off = "19:30" }
  stop_only = true
`, attach), ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(sched, plancheck.ResourceActionCreate)}}},
		},
		CheckDestroy: func(*terraform.State) error {
			p.mu.Lock()
			defer p.mu.Unlock()
			if len(p.schedules) != 0 || p.onSched != 0 {
				return fmt.Errorf("left: %d schedules, instance on %d", len(p.schedules), p.onSched)
			}
			return nil
		},
	})
}

// A schedule with the same name is never taken over (names aren't unique, and it may be another
// config's): the create stops and says to import it.
func TestPowerSchedule_ExistingName(t *testing.T) {
	f := newFakeAPI(t)
	p := newPowerFake(f)
	p.schedules[4] = map[string]any{"id": int64(4), "name": "office-hours", "enabled": true, "scheduleType": "power", "scheduleTimezone": "UTC", "description": nil}
	for _, d := range weekdayNames {
		p.schedules[4][d+"OnTime"], p.schedules[4][d+"OffTime"] = "00:00", "24:00"
	}
	cfg := f.providerConfig() + `
resource "mtncloud_power_schedule" "office" {
  name = "office-hours"
}
`
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories(),
		Steps:                    []resource.TestStep{{Config: cfg, ExpectError: regexp.MustCompile(`(?s)already exists \(ID 4\).*terraform import`)}},
		CheckDestroy: func(*terraform.State) error {
			if p.posts != 0 || len(p.schedules) != 1 {
				return fmt.Errorf("%d created, %d schedules", p.posts, len(p.schedules))
			}
			return nil
		},
	})
}

// Plan-time checks on the other attributes, and a day known only after apply.
func TestPowerSchedule_Inputs(t *testing.T) {
	f := newFakeAPI(t)
	newPowerFake(f)
	cfg := func(body string) string {
		return f.providerConfig() + `
resource "terraform_data" "hours" {
  input = { on = "07:00", off = "19:00" }
}
resource "mtncloud_power_schedule" "s" {
  name = "office-hours"
` + body + `}
`
	}
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories(),
		Steps: []resource.TestStep{
			{Config: cfg(`  description = ""
`), ExpectError: regexp.MustCompile(`string length must be at least 1`)},
			{Config: cfg(`  time_zone = "Africa/Lagoss"
`), ExpectError: regexp.MustCompile(`isn't a time zone name`)},
			// The day is unknown at plan time: it is checked when it is known, not refused.
			{Config: cfg(`  monday = terraform_data.hours.output
`), Check: resource.TestCheckResourceAttr("mtncloud_power_schedule.s", "monday.off", "19:00")},
		},
		CheckDestroy: func(*terraform.State) error { return nil },
	})
}

// Destroying the attachment after the instance was moved to another schedule elsewhere leaves it
// there; and an instance that is gone plans the attachment again.
func TestInstancePowerSchedule_Moved(t *testing.T) {
	f := newFakeAPI(t)
	p := newPowerFake(f)
	sched := f.providerConfig() + `
resource "mtncloud_power_schedule" "office" {
  name = "office-hours"
}
`
	attach := sched + `
resource "mtncloud_instance_power_schedule" "web" {
  instance_id       = "4660"
  power_schedule_id = mtncloud_power_schedule.office.id
}
`
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories(),
		Steps: []resource.TestStep{
			{Config: attach},
			{PreConfig: func() { p.mu.Lock(); p.gone = true; p.mu.Unlock() }, Config: attach, PlanOnly: true, ExpectNonEmptyPlan: true},
			{PreConfig: func() { p.mu.Lock(); p.gone = false; p.onSched = 99; p.mu.Unlock() }, Config: sched,
				Check: func(*terraform.State) error {
					p.mu.Lock()
					defer p.mu.Unlock()
					if p.onSched != 99 {
						return fmt.Errorf("the instance was taken off schedule 99, which this resource never set (now %d)", p.onSched)
					}
					p.onSched = 0
					return nil
				}},
		},
		CheckDestroy: func(*terraform.State) error { return nil },
	})
}
