package client

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// Weekdays are the power schedule's days, in the API's field order (mondayOn, ... sundayOff).
var Weekdays = []string{"monday", "tuesday", "wednesday", "thursday", "friday", "saturday", "sunday"}

// PowerSchedule is a weekly plan of when instances on it run. Each day has an on and an off time
// ("HH:MM", 24-hour; off may be "24:00"). A day the API isn't sent keeps its value, and a new
// schedule's days default to on all day, 00:00 to 24:00. A day off all day is 00:00 to 00:00.
// Live QA 2026-10-31 (t114): minutes are kept; an off time before the on time is refused; the time
// zone is stored as sent; an empty description clears it; a schedule in use can't be deleted.
type PowerSchedule struct {
	ID          int64
	Name        string
	Description string
	Enabled     bool
	StopOnly    bool // scheduleType "power off": the schedule only stops instances, never starts them
	TimeZone    string
	Days        map[string]DayHours // keyed by Weekdays
	HoursSaved  float64             // totalMonthlyHoursSaved, computed by MTN Cloud
}

// DayHours is one day's on and off time.
type DayHours struct{ On, Off string }

// AllDay is a day the schedule doesn't stop instances: the API's default.
var AllDay = DayHours{On: "00:00", Off: "24:00"}

func scheduleType(stopOnly bool) string {
	if stopOnly {
		return "power off"
	}
	return "power"
}

func powerSchedulesPath() string { return "/api/power-schedules" }

func toPowerSchedule(w map[string]any) *PowerSchedule {
	s := &PowerSchedule{Days: map[string]DayHours{}}
	s.ID = int64(num(w["id"]))
	s.Name, _ = w["name"].(string)
	s.Description, _ = w["description"].(string)
	s.Enabled, _ = w["enabled"].(bool)
	t, _ := w["scheduleType"].(string)
	s.StopOnly = strings.EqualFold(t, "power off")
	s.TimeZone, _ = w["scheduleTimezone"].(string)
	s.HoursSaved = num(w["totalMonthlyHoursSaved"])
	for _, d := range Weekdays {
		on, _ := w[d+"OnTime"].(string)
		off, _ := w[d+"OffTime"].(string)
		if on == "" && off == "" {
			s.Days[d] = AllDay
			continue
		}
		s.Days[d] = DayHours{On: on, Off: off}
	}
	return s
}

func num(v any) float64 {
	switch n := v.(type) {
	case float64:
		return n
	case string:
		f, _ := strconv.ParseFloat(n, 64)
		return f
	}
	return 0
}

// hour is the whole hour of an "HH:MM" time, which the API also wants as mondayOn and so on.
func hour(t string) int {
	h, _ := strconv.Atoi(strings.SplitN(t, ":", 2)[0])
	return h
}

// powerScheduleBody sends every field, so an update leaves nothing behind (the API's PUT keeps
// fields it isn't sent). A day missing from s.Days is sent as on all day.
func powerScheduleBody(s PowerSchedule) map[string]any {
	b := map[string]any{
		"name":             s.Name,
		"description":      s.Description, // "" clears it
		"enabled":          s.Enabled,
		"scheduleType":     scheduleType(s.StopOnly),
		"scheduleTimezone": s.TimeZone,
	}
	for _, d := range Weekdays {
		h, ok := s.Days[d]
		if !ok {
			h = AllDay
		}
		b[d+"On"], b[d+"OnTime"] = hour(h.On), h.On
		b[d+"Off"], b[d+"OffTime"] = hour(h.Off), h.Off
	}
	return map[string]any{"schedule": b}
}

// PowerSchedule reads one schedule. One that is gone is a not-found error.
func (c *Client) PowerSchedule(ctx context.Context, id int64) (*PowerSchedule, error) {
	var resp struct {
		Schedule map[string]any `json:"schedule"`
	}
	p := powerSchedulesPath() + "/" + itoa(id)
	if err := c.get(ctx, p, nil, &resp); err != nil {
		return nil, err
	}
	if resp.Schedule == nil {
		return nil, &APIError{Method: http.MethodGet, Path: p, StatusCode: http.StatusNotFound}
	}
	return toPowerSchedule(resp.Schedule), nil
}

// PowerSchedulesNamed lists the schedules with exactly this name (case ignored).
func (c *Client) PowerSchedulesNamed(ctx context.Context, name string) ([]PowerSchedule, error) {
	var resp struct {
		Schedules []map[string]any `json:"schedules"`
	}
	q := url.Values{"name": {name}, "max": {"100"}}
	if err := c.get(ctx, powerSchedulesPath(), q, &resp); err != nil {
		return nil, err
	}
	var out []PowerSchedule
	for _, w := range resp.Schedules {
		if s := toPowerSchedule(w); strings.EqualFold(s.Name, name) {
			out = append(out, *s)
		}
	}
	return out, nil
}

// CreatePowerSchedule makes a schedule.
func (c *Client) CreatePowerSchedule(ctx context.Context, s PowerSchedule) (*PowerSchedule, error) {
	var resp struct {
		Schedule map[string]any `json:"schedule"`
	}
	if err := c.do(ctx, http.MethodPost, powerSchedulesPath(), nil, powerScheduleBody(s), &resp); err != nil {
		return nil, err
	}
	if resp.Schedule == nil {
		return nil, fmt.Errorf("MTN Cloud created power schedule %q but didn't return it", s.Name)
	}
	return toPowerSchedule(resp.Schedule), nil
}

// UpdatePowerSchedule replaces a schedule's settings.
func (c *Client) UpdatePowerSchedule(ctx context.Context, id int64, s PowerSchedule) (*PowerSchedule, error) {
	var resp struct {
		Schedule map[string]any `json:"schedule"`
	}
	if err := c.do(ctx, http.MethodPut, powerSchedulesPath()+"/"+itoa(id), nil, powerScheduleBody(s), &resp); err != nil {
		return nil, err
	}
	if resp.Schedule == nil {
		return c.PowerSchedule(ctx, id)
	}
	return toPowerSchedule(resp.Schedule), nil
}

// DeletePowerSchedule removes a schedule. One already gone is not an error; one still used by an
// instance is refused by MTN Cloud (400 "currently in use").
func (c *Client) DeletePowerSchedule(ctx context.Context, id int64) error {
	err := c.do(ctx, http.MethodDelete, powerSchedulesPath()+"/"+itoa(id), nil, nil, nil)
	if IsNotFound(err) {
		return nil
	}
	return err
}

// InstancePowerSchedule is the ID of the power schedule an instance is on, or 0.
func (c *Client) InstancePowerSchedule(ctx context.Context, instance int64) (int64, error) {
	var resp struct {
		Instance *struct {
			PowerSchedule *struct {
				Type *struct {
					ID int64 `json:"id"`
				} `json:"type"`
			} `json:"powerSchedule"`
		} `json:"instance"`
	}
	p := "/api/instances/" + itoa(instance)
	if err := c.get(ctx, p, nil, &resp); err != nil {
		return 0, err
	}
	if resp.Instance == nil {
		return 0, &APIError{Method: http.MethodGet, Path: p, StatusCode: http.StatusNotFound}
	}
	if resp.Instance.PowerSchedule == nil || resp.Instance.PowerSchedule.Type == nil {
		return 0, nil
	}
	return resp.Instance.PowerSchedule.Type.ID, nil
}

// SetInstancePowerSchedule puts an instance on a schedule, or takes it off with 0. Only this field
// is sent: an instance update without it keeps the schedule (live QA 2026-10-31, t114).
func (c *Client) SetInstancePowerSchedule(ctx context.Context, instance, schedule int64) error {
	var v any // null takes the instance off its schedule
	if schedule > 0 {
		v = schedule
	}
	body := map[string]any{"instance": map[string]any{"powerScheduleType": v}}
	return c.do(ctx, http.MethodPut, "/api/instances/"+itoa(instance), nil, body, nil)
}
