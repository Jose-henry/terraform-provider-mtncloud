package client

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"testing"
)

func TestInstancePowerSchedule(t *testing.T) {
	ctx := context.Background()
	f := newFakeAPI(t)
	var sent []map[string]map[string]any
	on := `{"instance":{"id":4652,"powerSchedule":{"type":{"id":5,"name":"office"},"timezone":null}}}`
	off := `{"instance":{"id":4652,"powerSchedule":null}}`
	cur := on
	f.route("GET", "/api/instances/4652", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(cur)) })
	f.route("PUT", "/api/instances/4652", func(w http.ResponseWriter, r *http.Request) {
		var b map[string]map[string]any
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &b)
		sent = append(sent, b)
		_, _ = w.Write([]byte(off))
	})
	c := f.client(t)
	if id, err := c.InstancePowerSchedule(ctx, 4652); err != nil || id != 5 {
		t.Fatalf("on a schedule: %d, %v", id, err)
	}
	cur = off
	if id, err := c.InstancePowerSchedule(ctx, 4652); err != nil || id != 0 {
		t.Fatalf("on none: %d, %v", id, err)
	}
	if err := c.SetInstancePowerSchedule(ctx, 4652, 5); err != nil {
		t.Fatal(err)
	}
	if err := c.SetInstancePowerSchedule(ctx, 4652, 0); err != nil {
		t.Fatal(err)
	}
	if len(sent) != 2 || sent[0]["instance"]["powerScheduleType"] != float64(5) {
		t.Fatalf("sent %v", sent)
	}
	v, present := sent[1]["instance"]["powerScheduleType"]
	if !present || v != nil {
		t.Errorf("taking it off must send powerScheduleType null: %v", sent[1])
	}
	if len(sent[0]["instance"]) != 1 {
		t.Errorf("only powerScheduleType is sent, so nothing else changes: %v", sent[0])
	}
}

func TestPowerScheduleBody(t *testing.T) {
	b := powerScheduleBody(PowerSchedule{Name: "office", Enabled: true, StopOnly: true, TimeZone: "UTC",
		Days: map[string]DayHours{"monday": {On: "07:00", Off: "19:30"}, "sunday": {On: "00:00", Off: "00:00"}}})["schedule"].(map[string]any)
	if b["scheduleType"] != "power off" || b["mondayOn"] != 7 || b["mondayOff"] != 19 || b["mondayOffTime"] != "19:30" {
		t.Errorf("monday %v", b)
	}
	if b["tuesdayOnTime"] != "00:00" || b["tuesdayOffTime"] != "24:00" || b["tuesdayOff"] != 24 {
		t.Errorf("a day left out is sent on all day: %v", b)
	}
	if b["sundayOffTime"] != "00:00" || b["description"] != "" {
		t.Errorf("sunday off, description cleared: %v", b)
	}
	s := toPowerSchedule(map[string]any{"id": 5.0, "name": "office", "scheduleType": "power off", "totalMonthlyHoursSaved": 49.3,
		"mondayOnTime": "07:00", "mondayOffTime": "19:30"})
	if s.ID != 5 || !s.StopOnly || s.Days["monday"].Off != "19:30" || s.Days["friday"] != AllDay || s.HoursSaved != 49.3 {
		t.Errorf("read %+v", s)
	}
}
