package client

import (
	"context"
	"fmt"
	"net/http"
)

// ScaleSchedule is an instance's scaling for part of the week (or between two dates): while it is
// on, its Threshold replaces the instance's own. MTN Cloud copies a library threshold's values into
// it when the schedule is created or updated with that threshold, and keeps no link to it: a later
// change to the library threshold doesn't reach the schedule (live QA 2026-10-31, t108).
type ScaleSchedule struct {
	ID        int64
	Type      string // "dayOfWeek" or "exact"
	TimeZone  string
	StartDay  int64 // 1 (Sunday) to 7 (Saturday)
	EndDay    int64
	StartTime string // "HH:MM", 24-hour
	EndTime   string
	StartDate string // RFC 3339, for "exact"
	EndDate   string
	Threshold Threshold
}

// ScaleScheduleSpec is what Create and Update send. ThresholdID is the library threshold whose
// values the schedule copies (sourceThresholdId); 0 on an update keeps the values it has.
type ScaleScheduleSpec struct {
	Type               string
	TimeZone           string
	StartDay, EndDay   int64
	StartTime, EndTime string
	StartDate, EndDate string
	ThresholdID        int64
}

type wireScaleSchedule struct {
	ID               int64          `json:"id"`
	ScheduleType     string         `json:"scheduleType"`
	ScheduleTimezone string         `json:"scheduleTimezone"`
	StartDayOfWeek   int64          `json:"startDayOfWeek"`
	EndDayOfWeek     int64          `json:"endDayOfWeek"`
	StartTime        string         `json:"startTime"`
	EndTime          string         `json:"endTime"`
	StartDate        string         `json:"startDate"`
	EndDate          string         `json:"endDate"`
	Threshold        *wireThreshold `json:"threshold"`
}

func (w *wireScaleSchedule) toSchedule() *ScaleSchedule {
	s := &ScaleSchedule{
		ID: w.ID, Type: w.ScheduleType, TimeZone: w.ScheduleTimezone,
		StartDay: w.StartDayOfWeek, EndDay: w.EndDayOfWeek, StartTime: w.StartTime, EndTime: w.EndTime,
		StartDate: w.StartDate, EndDate: w.EndDate,
	}
	if w.Threshold != nil {
		s.Threshold = w.Threshold.toThreshold()
	}
	return s
}

func (w *wireThreshold) toThreshold() Threshold {
	return Threshold{
		AutoUp: w.AutoUp, AutoDown: w.AutoDown,
		MinCount: orZero(w.MinCount), MaxCount: orZero(w.MaxCount), Increment: orZero(w.ScaleIncrement),
		CPU:    rangeOf(w.CPUEnabled, w.MinCPU, w.MaxCPU),
		Memory: rangeOf(w.MemoryEnabled, w.MinMemory, w.MaxMemory),
		Disk:   rangeOf(w.DiskEnabled, w.MinDisk, w.MaxDisk),
	}
}

func schedulesPath(instance int64) string { return "/api/instances/" + itoa(instance) + "/schedules" }

// ScaleSchedules lists an instance's scaling schedules.
func (c *Client) ScaleSchedules(ctx context.Context, instance int64) ([]ScaleSchedule, error) {
	var resp struct {
		InstanceSchedules []wireScaleSchedule `json:"instanceSchedules"`
	}
	if err := c.get(ctx, schedulesPath(instance), nil, &resp); err != nil {
		return nil, err
	}
	out := make([]ScaleSchedule, 0, len(resp.InstanceSchedules))
	for i := range resp.InstanceSchedules {
		out = append(out, *resp.InstanceSchedules[i].toSchedule())
	}
	return out, nil
}

// ScaleSchedule reads one schedule. A schedule or instance that is gone is a not-found error.
func (c *Client) ScaleSchedule(ctx context.Context, instance, id int64) (*ScaleSchedule, error) {
	var resp struct {
		InstanceSchedule *wireScaleSchedule `json:"instanceSchedule"`
	}
	p := schedulesPath(instance) + "/" + itoa(id)
	if err := c.get(ctx, p, nil, &resp); err != nil {
		return nil, err
	}
	if resp.InstanceSchedule == nil {
		return nil, &APIError{Method: http.MethodGet, Path: p, StatusCode: http.StatusNotFound}
	}
	return resp.InstanceSchedule.toSchedule(), nil
}

func (s ScaleScheduleSpec) body() map[string]any {
	b := map[string]any{"scheduleType": s.Type}
	if s.ThresholdID > 0 { // 0 keeps the values the schedule has
		b["threshold"] = map[string]any{"sourceThresholdId": s.ThresholdID}
	}
	if s.Type == "exact" {
		b["startDate"], b["endDate"] = s.StartDate, s.EndDate
	} else {
		b["startDayOfWeek"], b["endDayOfWeek"] = s.StartDay, s.EndDay
		b["startTime"], b["endTime"] = s.StartTime, s.EndTime
		if s.TimeZone != "" {
			b["scheduleTimezone"] = s.TimeZone
		}
	}
	return map[string]any{"instanceSchedule": b}
}

// CreateScaleSchedule adds a schedule that copies a library threshold's values.
func (c *Client) CreateScaleSchedule(ctx context.Context, instance int64, s ScaleScheduleSpec) (*ScaleSchedule, error) {
	var resp struct {
		InstanceSchedule *wireScaleSchedule `json:"instanceSchedule"`
	}
	if err := c.do(ctx, http.MethodPost, schedulesPath(instance), nil, s.body(), &resp); err != nil {
		return nil, err
	}
	if resp.InstanceSchedule == nil {
		return nil, fmt.Errorf("MTN Cloud created a scale schedule on instance %d but didn't return it", instance)
	}
	return resp.InstanceSchedule.toSchedule(), nil
}

// UpdateScaleSchedule replaces a schedule's times and copies the library threshold's values again
// (sending sourceThresholdId re-copies them, live QA 2026-10-31).
func (c *Client) UpdateScaleSchedule(ctx context.Context, instance, id int64, s ScaleScheduleSpec) (*ScaleSchedule, error) {
	var resp struct {
		InstanceSchedule *wireScaleSchedule `json:"instanceSchedule"`
	}
	if err := c.do(ctx, http.MethodPut, schedulesPath(instance)+"/"+itoa(id), nil, s.body(), &resp); err != nil {
		return nil, err
	}
	if resp.InstanceSchedule == nil {
		return c.ScaleSchedule(ctx, instance, id)
	}
	return resp.InstanceSchedule.toSchedule(), nil
}

// DeleteScaleSchedule removes a schedule. One already gone is not an error.
func (c *Client) DeleteScaleSchedule(ctx context.Context, instance, id int64) error {
	err := c.do(ctx, http.MethodDelete, schedulesPath(instance)+"/"+itoa(id), nil, nil, nil)
	if IsNotFound(err) {
		return nil
	}
	return err
}

// ScaleThreshold reads a library threshold (mtncloud_scale_threshold): the values a schedule copies.
func (c *Client) ScaleThreshold(ctx context.Context, id int64) (*Threshold, error) {
	var resp struct {
		ScaleThreshold *wireThreshold `json:"scaleThreshold"`
	}
	p := "/api/scale-thresholds/" + itoa(id)
	if err := c.get(ctx, p, nil, &resp); err != nil {
		return nil, err
	}
	if resp.ScaleThreshold == nil {
		return nil, &APIError{Method: http.MethodGet, Path: p, StatusCode: http.StatusNotFound}
	}
	t := resp.ScaleThreshold.toThreshold()
	return &t, nil
}
