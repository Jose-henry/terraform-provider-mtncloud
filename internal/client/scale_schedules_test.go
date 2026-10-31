package client

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

func instancesFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "instances", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestScaleSchedules(t *testing.T) {
	ctx := context.Background()
	created := instancesFixture(t, "scale_schedule_create.json")

	t.Run("create copies a library threshold", func(t *testing.T) {
		f := newFakeAPI(t)
		var sent map[string]map[string]any
		f.route("POST", "/api/instances/7/schedules", func(w http.ResponseWriter, r *http.Request) {
			b, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(b, &sent)
			_, _ = w.Write(created)
		})
		s, err := f.client(t).CreateScaleSchedule(ctx, 7, ScaleScheduleSpec{
			Type: "dayOfWeek", TimeZone: "Africa/Lagos", StartDay: 2, EndDay: 6, StartTime: "08:00", EndTime: "18:00", ThresholdID: 70,
		})
		if err != nil {
			t.Fatal(err)
		}
		body := sent["instanceSchedule"]
		if src := body["threshold"].(map[string]any)["sourceThresholdId"]; src != float64(70) {
			t.Errorf("sourceThresholdId %v, want 70", src)
		}
		if body["startDayOfWeek"] != float64(2) || body["endTime"] != "18:00" || body["scheduleTimezone"] != "Africa/Lagos" {
			t.Errorf("body %v", body)
		}
		if _, ok := body["startDate"]; ok {
			t.Error("a day-of-week schedule sent startDate")
		}
		if s.ID != 3 || s.StartDay != 2 || s.EndTime != "18:00" || s.TimeZone != "Africa/Lagos" {
			t.Errorf("schedule %+v", s)
		}
		if th := s.Threshold; !th.AutoUp || th.MaxCount != 3 || th.CPU == nil || th.CPU.Max != 80 || th.Memory != nil {
			t.Errorf("copied threshold %+v", th)
		}
	})

	t.Run("exact dates", func(t *testing.T) {
		f := newFakeAPI(t)
		var sent map[string]map[string]any
		f.route("POST", "/api/instances/7/schedules", func(w http.ResponseWriter, r *http.Request) {
			b, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(b, &sent)
			_, _ = w.Write(created)
		})
		_, err := f.client(t).CreateScaleSchedule(ctx, 7, ScaleScheduleSpec{
			Type: "exact", StartDate: "2026-12-24T00:00:00Z", EndDate: "2026-12-27T00:00:00Z", ThresholdID: 70,
		})
		if err != nil {
			t.Fatal(err)
		}
		body := sent["instanceSchedule"]
		if body["startDate"] != "2026-12-24T00:00:00Z" || body["scheduleType"] != "exact" {
			t.Errorf("body %v", body)
		}
		if _, ok := body["startDayOfWeek"]; ok {
			t.Error("an exact schedule sent startDayOfWeek")
		}
	})

	t.Run("read, list and threshold", func(t *testing.T) {
		f := newFakeAPI(t)
		f.route("GET", "/api/instances/7/schedules/3", func(w http.ResponseWriter, _ *http.Request) {
			var all struct {
				InstanceSchedule json.RawMessage `json:"instanceSchedule"`
			}
			_ = json.Unmarshal(created, &all)
			_, _ = w.Write([]byte(`{"instanceSchedule":` + string(all.InstanceSchedule) + `}`))
		})
		f.route("GET", "/api/instances/7/schedules", func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write(instancesFixture(t, "scale_schedules.json"))
		})
		f.route("GET", "/api/scale-thresholds/70", func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write(instancesFixture(t, "scale_threshold.json"))
		})
		c := f.client(t)
		s, err := c.ScaleSchedule(ctx, 7, 3)
		if err != nil || s.ID != 3 {
			t.Fatalf("read: %v, %+v", err, s)
		}
		all, err := c.ScaleSchedules(ctx, 7)
		if err != nil || len(all) != 1 || all[0].EndDay != 6 {
			t.Fatalf("list: %v, %+v", err, all)
		}
		th, err := c.ScaleThreshold(ctx, 70)
		if err != nil || th.CPU == nil || th.CPU.Max != 70 {
			t.Fatalf("threshold: %v, %+v", err, th)
		}
		if _, err := c.ScaleSchedule(ctx, 7, 4); !IsNotFound(err) {
			t.Errorf("a missing schedule: %v, want not found", err)
		}
	})

	t.Run("update sends the threshold again; delete of a gone schedule is fine", func(t *testing.T) {
		f := newFakeAPI(t)
		var sent map[string]map[string]any
		f.route("PUT", "/api/instances/7/schedules/3", func(w http.ResponseWriter, r *http.Request) {
			b, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(b, &sent)
			_, _ = w.Write(created)
		})
		c := f.client(t)
		if _, err := c.UpdateScaleSchedule(ctx, 7, 3, ScaleScheduleSpec{Type: "dayOfWeek", StartDay: 1, EndDay: 7, StartTime: "00:00", EndTime: "23:59", ThresholdID: 71}); err != nil {
			t.Fatal(err)
		}
		if src := sent["instanceSchedule"]["threshold"].(map[string]any)["sourceThresholdId"]; src != float64(71) {
			t.Errorf("update sent sourceThresholdId %v, want 71 (it re-copies the values)", src)
		}
		if err := c.DeleteScaleSchedule(ctx, 7, 3); err != nil {
			t.Errorf("delete of a schedule that's gone: %v, want nil", err)
		}
	})
}
