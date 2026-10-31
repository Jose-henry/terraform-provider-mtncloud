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
	"github.com/hashicorp/terraform-plugin-testing/knownvalue"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"
)

// schedFake is instance 4650's scale schedules and the library thresholds they copy from. Like MTN
// Cloud (live QA 2026-10-31), a schedule copies a threshold's values when sent sourceThresholdId and
// keeps no link to it afterwards.
type schedFake struct {
	mu         sync.Mutex
	thresholds map[int64]map[string]any
	schedules  map[int64]map[string]any
	next       int64
	posts      int
	puts       int
	bumpOnPut  bool // the library threshold 71 changes just before the next PUT, as when it is applied first
}

func newSchedFake(t *testing.T, f *fakeAPI) *schedFake {
	s := &schedFake{
		thresholds: map[int64]map[string]any{
			70: {"autoUp": true, "autoDown": true, "minCount": 1, "maxCount": 3, "cpuEnabled": true, "minCpu": 10.0, "maxCpu": 80.0},
			71: {"autoUp": true, "autoDown": false, "minCount": 2, "maxCount": 5, "memoryEnabled": true, "minMemory": 20.0, "maxMemory": 90.0},
		},
		schedules: map[int64]map[string]any{},
		next:      2,
	}
	f.mu.Lock()
	f.sched = s
	f.mu.Unlock()
	return s
}

func (s *schedFake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	write := func(v any) { b, _ := json.Marshal(v); _, _ = w.Write(b) }
	var id int64
	if _, err := fmt.Sscanf(r.URL.Path, "/api/scale-thresholds/%d", &id); err == nil {
		th, ok := s.thresholds[id]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		write(map[string]any{"scaleThreshold": th})
		return
	}
	if !strings.HasPrefix(r.URL.Path, "/api/instances/4650/schedules") {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	_, _ = fmt.Sscanf(r.URL.Path, "/api/instances/4650/schedules/%d", &id)
	switch r.Method {
	case http.MethodGet:
		if id == 0 {
			list := []map[string]any{}
			for _, sc := range s.schedules {
				list = append(list, sc)
			}
			write(map[string]any{"instanceSchedules": list})
			return
		}
		sc, ok := s.schedules[id]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		write(map[string]any{"instanceSchedule": sc})
	case http.MethodPost, http.MethodPut:
		raw, _ := io.ReadAll(r.Body)
		var body struct {
			InstanceSchedule map[string]any `json:"instanceSchedule"`
		}
		_ = json.Unmarshal(raw, &body)
		in := body.InstanceSchedule
		if r.Method == http.MethodPost {
			s.posts++
			s.next++
			id = s.next
			s.schedules[id] = map[string]any{"id": id, "scheduleTimezone": "UTC"}
		} else {
			s.puts++
			if s.bumpOnPut {
				s.bumpOnPut = false
				s.thresholds[71]["maxCount"] = 9
			}
		}
		sc := s.schedules[id]
		for k, v := range in {
			if k != "threshold" {
				sc[k] = v
			}
		}
		if th, ok := in["threshold"].(map[string]any); ok {
			src := int64(th["sourceThresholdId"].(float64))
			copied := map[string]any{}
			for k, v := range s.thresholds[src] {
				copied[k] = v
			}
			sc["threshold"] = copied
		}
		write(map[string]any{"success": true, "instanceSchedule": sc})
	case http.MethodDelete:
		delete(s.schedules, id)
		write(map[string]any{"success": true})
	}
}

func TestInstanceScaleSchedule(t *testing.T) {
	f := newFakeAPI(t)
	s := newSchedFake(t, f)
	cfg := func(threshold, body string) string {
		return f.providerConfig() + `
resource "mtncloud_instance_scale_schedule" "work" {
  instance_id        = "4650"
  scale_threshold_id = "` + threshold + `"
` + body + `}
`
	}
	weekdays := `  time_zone  = "Africa/Lagos"
  start_day  = "monday"
  start_time = "08:00"
  end_day    = "friday"
  end_time   = "18:00"
`
	const res = "mtncloud_instance_scale_schedule.work"
	update := resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(res, plancheck.ResourceActionUpdate)}}
	empty := resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()}}
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories(),
		Steps: []resource.TestStep{
			{Config: cfg("70", `  start_day = "monday"
  start_date = "2026-12-24T00:00:00Z"
`), ExpectError: regexp.MustCompile(`Weekly times and dates together`)},
			{Config: cfg("70", `  start_day = "monday"
`), ExpectError: regexp.MustCompile(`Weekly schedule incomplete`)},
			{
				Config: cfg("70", weekdays),
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckResourceAttr(res, "id", "4650/3"),
					resource.TestCheckResourceAttr(res, "threshold.max_cpu_percentage", "80"),
					resource.TestCheckResourceAttr(res, "threshold.max_count", "3"),
					resource.TestCheckNoResourceAttr(res, "threshold.max_memory_percentage"),
					func(*terraform.State) error {
						s.mu.Lock()
						defer s.mu.Unlock()
						if sc := s.schedules[3]; sc["startDayOfWeek"] != float64(2) || sc["endDayOfWeek"] != float64(6) {
							t.Errorf("days sent %v and %v, want 2 (Monday) and 6 (Friday)", sc["startDayOfWeek"], sc["endDayOfWeek"])
						}
						return nil
					},
				),
			},
			{Config: cfg("70", weekdays), ConfigPlanChecks: empty},
			{
				// Another threshold: copied in place.
				Config: cfg("71", weekdays), ConfigPlanChecks: update,
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckResourceAttr(res, "threshold.max_memory_percentage", "90"),
					resource.TestCheckNoResourceAttr(res, "threshold.max_cpu_percentage"),
				),
			},
			{
				// The library threshold changes outside the schedule: the plan copies it again.
				PreConfig: func() {
					s.mu.Lock()
					s.thresholds[71]["maxCount"] = 8
					s.mu.Unlock()
				},
				Config: cfg("71", weekdays), ConfigPlanChecks: update,
				Check: resource.TestCheckResourceAttr(res, "threshold.max_count", "8"),
			},
			{Config: cfg("71", weekdays), ConfigPlanChecks: empty},
			{
				// New times, same threshold: an update that keeps the copied values.
				Config: cfg("71", strings.Replace(weekdays, `"18:00"`, `"19:30"`, 1)), ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction(res, plancheck.ResourceActionUpdate),
					plancheck.ExpectKnownValue(res, tfjsonpath.New("threshold").AtMapKey("max_count"), knownvalue.Int64Exact(8)),
				}},
			},
			{
				// The threshold changes between plan and apply, with new times: the update keeps the
				// copied values it planned, and the next plan copies the new ones.
				PreConfig: func() { s.mu.Lock(); s.bumpOnPut = true; s.mu.Unlock() },
				Config:    cfg("71", strings.Replace(weekdays, `"18:00"`, `"20:00"`, 1)), ConfigPlanChecks: update,
				Check:              resource.TestCheckResourceAttr(res, "threshold.max_count", "8"),
				ExpectNonEmptyPlan: true, // the refresh sees the newer threshold
			},
			{
				Config: cfg("71", strings.Replace(weekdays, `"18:00"`, `"20:00"`, 1)), ConfigPlanChecks: update,
				Check: resource.TestCheckResourceAttr(res, "threshold.max_count", "9"),
			},
			{Config: cfg("71", strings.Replace(weekdays, `"18:00"`, `"20:00"`, 1)), ConfigPlanChecks: empty},
			{
				ResourceName: res, ImportState: true, ImportStateVerify: true,
				ImportStateVerifyIgnore: []string{"scale_threshold_id"}, // not recorded on the schedule
				Config:                  cfg("71", strings.Replace(weekdays, `"18:00"`, `"20:00"`, 1)),
			},
			{
				// The library threshold is deleted: the plan sets it again (and applying would fail).
				PreConfig: func() { s.mu.Lock(); delete(s.thresholds, 71); s.mu.Unlock() },
				Config:    cfg("71", strings.Replace(weekdays, `"18:00"`, `"20:00"`, 1)), PlanOnly: true, ExpectNonEmptyPlan: true,
			},
			{
				// Deleted outside Terraform: made again.
				PreConfig: func() {
					s.mu.Lock()
					s.thresholds[71] = map[string]any{"autoUp": true, "minCount": 1, "maxCount": 2}
					clear(s.schedules)
					s.mu.Unlock()
				},
				Config: cfg("71", strings.Replace(weekdays, `"18:00"`, `"20:00"`, 1)),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction(res, plancheck.ResourceActionCreate)}},
			},
		},
		CheckDestroy: func(*terraform.State) error {
			s.mu.Lock()
			defer s.mu.Unlock()
			if len(s.schedules) != 0 {
				t.Errorf("schedules left: %v", s.schedules)
			}
			return nil
		},
	})
	if s.posts != 2 {
		t.Errorf("%d creates, want 2 (the first, and after the out-of-band delete)", s.posts)
	}
}

// A schedule with the same times and copied values already on the instance (a create that went
// through before a retry) is adopted, not made twice; dated schedules work too.
func TestInstanceScaleSchedule_AdoptAndDates(t *testing.T) {
	f := newFakeAPI(t)
	s := newSchedFake(t, f)
	s.schedules[2] = map[string]any{"id": 2, "scheduleType": "exact", "startDate": "2026-12-24T00:00:00Z", "endDate": "2026-12-27T00:00:00Z",
		"scheduleTimezone": "UTC", "threshold": s.thresholds[70]}
	const res = "mtncloud_instance_scale_schedule.xmas"
	cfg := f.providerConfig() + `
resource "mtncloud_instance_scale_schedule" "xmas" {
  instance_id        = "4650"
  scale_threshold_id = "70"
  start_date         = "2026-12-24T01:00:00+01:00"
  end_date           = "2026-12-27T00:00:00Z"
}
`
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories(),
		Steps: []resource.TestStep{
			{Config: cfg, Check: resource.ComposeTestCheckFunc(
				resource.TestCheckResourceAttr(res, "id", "4650/2"),
				resource.TestCheckResourceAttr(res, "start_date", "2026-12-24T01:00:00+01:00"),
				resource.TestCheckResourceAttr(res, "threshold.max_cpu_percentage", "80"),
			)},
			{Config: cfg, ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()}}},
		},
	})
	if s.posts != 0 {
		t.Errorf("%d creates, want the existing schedule adopted", s.posts)
	}
}

// A schedule with the same times but another threshold's values is someone else's: left alone.
func TestInstanceScaleSchedule_NoAdoptOfOthers(t *testing.T) {
	f := newFakeAPI(t)
	s := newSchedFake(t, f)
	s.schedules[2] = map[string]any{"id": 2, "scheduleType": "dayOfWeek", "scheduleTimezone": "UTC",
		"startDayOfWeek": 2, "startTime": "08:00", "endDayOfWeek": 6, "endTime": "18:00", "threshold": s.thresholds[71]}
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories(),
		Steps: []resource.TestStep{{
			Config: f.providerConfig() + `
resource "mtncloud_instance_scale_schedule" "w" {
  instance_id        = "4650"
  scale_threshold_id = "70"
  start_day          = "monday"
  start_time         = "08:00"
  end_day            = "friday"
  end_time           = "18:00"
}
`,
			Check: resource.ComposeTestCheckFunc(
				resource.TestCheckResourceAttr("mtncloud_instance_scale_schedule.w", "id", "4650/3"),
				resource.TestCheckResourceAttr("mtncloud_instance_scale_schedule.w", "time_zone", "UTC"),
			),
		}},
	})
	if got := s.schedules[2]["threshold"].(map[string]any)["maxCount"]; got != 5 {
		t.Errorf("the other schedule's copy changed: maxCount %v", got)
	}
}

func TestInstanceScaleSchedule_Validation(t *testing.T) {
	f := newFakeAPI(t)
	newSchedFake(t, f)
	cfg := func(body string) string {
		return f.providerConfig() + `
resource "mtncloud_instance_scale_schedule" "v" {
  instance_id        = "4650"
  scale_threshold_id = "70"
` + body + "}\n"
	}
	for _, c := range []struct{ body, want string }{
		{``, `No schedule`},
		{`  start_date = "2026-12-24T00:00:00Z"
`, `Dated schedule incomplete`},
		{`  start_date = "24/12/2026"
  end_date = "2026-12-27T00:00:00Z"
`, `Not an RFC 3339 time`},
		{`  start_date = "2026-12-27T00:00:00Z"
  end_date = "2026-12-24T00:00:00Z"
`, `end_date isn't after start_date`},
		{`  time_zone = "UTC"
  start_date = "2026-12-24T00:00:00Z"
  end_date = "2026-12-27T00:00:00Z"
`, `time_zone with dates`},
		{`  start_day = "Mon"
  start_time = "08:00"
  end_day = "friday"
  end_time = "18:00"
`, `value must be one of`},
		{`  start_day = "monday"
  start_time = "24:00"
  end_day = "friday"
  end_time = "18:00"
`, `must be a 24-hour time`},
	} {
		resource.UnitTest(t, resource.TestCase{
			ProtoV6ProviderFactories: factories(),
			Steps:                    []resource.TestStep{{Config: cfg(c.body), PlanOnly: true, ExpectError: regexp.MustCompile(c.want)}},
		})
	}
}
