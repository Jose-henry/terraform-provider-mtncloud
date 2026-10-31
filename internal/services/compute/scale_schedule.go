package compute

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"

	"github.com/mtn/terraform-provider-mtncloud/internal/client"
)

// scaleScheduleResource attaches a library scale threshold (mtncloud_scale_threshold) to an instance
// for part of the week, or between two dates. MTN Cloud copies the threshold's values into the
// schedule and keeps no link to it (live QA 2026-10-31), so Read compares the copy with the threshold
// and plans copying it again when they differ.
type scaleScheduleResource struct{ base }

func newScaleSchedule() resource.Resource { return &scaleScheduleResource{} }

var (
	_ resource.ResourceWithImportState    = (*scaleScheduleResource)(nil)
	_ resource.ResourceWithValidateConfig = (*scaleScheduleResource)(nil)
	_ resource.ResourceWithModifyPlan     = (*scaleScheduleResource)(nil)
)

type scaleScheduleModel struct {
	ID               types.String `tfsdk:"id"`
	InstanceID       types.String `tfsdk:"instance_id"`
	ScaleThresholdID types.String `tfsdk:"scale_threshold_id"`
	TimeZone         types.String `tfsdk:"time_zone"`
	StartDay         types.String `tfsdk:"start_day"`
	StartTime        types.String `tfsdk:"start_time"`
	EndDay           types.String `tfsdk:"end_day"`
	EndTime          types.String `tfsdk:"end_time"`
	StartDate        types.String `tfsdk:"start_date"`
	EndDate          types.String `tfsdk:"end_date"`
	Threshold        types.Object `tfsdk:"threshold"`
}

// weekdays maps a day name to MTN Cloud's day number: Sunday is 1, Saturday is 7.
var weekdays = []string{"sunday", "monday", "tuesday", "wednesday", "thursday", "friday", "saturday"}

func dayNumber(name string) int64 {
	for i, d := range weekdays {
		if strings.EqualFold(d, name) {
			return int64(i + 1)
		}
	}
	return 0
}

func dayName(n int64) string {
	if n < 1 || n > 7 {
		return ""
	}
	return weekdays[n-1]
}

var clockTime = regexp.MustCompile(`^([01][0-9]|2[0-3]):[0-5][0-9]$`)

// copiedThresholdTypes are the threshold values the schedule holds, named as on mtncloud_scale_threshold.
var copiedThresholdTypes = map[string]attr.Type{
	"auto_upscale": types.BoolType, "auto_downscale": types.BoolType,
	"min_count": types.Int64Type, "max_count": types.Int64Type,
	"min_cpu_percentage": types.Float64Type, "max_cpu_percentage": types.Float64Type,
	"min_memory_percentage": types.Float64Type, "max_memory_percentage": types.Float64Type,
	"min_disk_percentage": types.Float64Type, "max_disk_percentage": types.Float64Type,
}

func (r *scaleScheduleResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_instance_scale_schedule"
}

func (r *scaleScheduleResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	replace := []planmodifier.String{stringplanmodifier.RequiresReplace()}
	id := []validator.String{stringvalidator.RegexMatches(digits, "must be an ID")}
	day := []validator.String{stringvalidator.OneOf(weekdays...)}
	clock := []validator.String{stringvalidator.RegexMatches(clockTime, `must be a 24-hour time, for example "08:00" or "18:30"`)}
	float := func(what string) schema.Float64Attribute {
		return schema.Float64Attribute{Computed: true, Description: what + ", in percent; null when not watched."}
	}
	resp.Schema = schema.Schema{
		Description: "Applies a library scale threshold (`mtncloud_scale_threshold`) to an instance for part of the week, " +
			"or between two dates. While the schedule is on, its threshold replaces the instance's own scaling " +
			"(the `scale` block). MTN Cloud copies the threshold's values into the schedule: when the threshold " +
			"changes, the next plan shows `scale_threshold_id` set again, and applying copies the new values.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{Computed: true, PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
				Description: "`<instance_id>/<schedule_id>`."},
			"instance_id": schema.StringAttribute{Required: true, PlanModifiers: replace, Validators: id,
				Description: "ID of the instance, for example `mtncloud_instance.web.id`. Changing it makes a new schedule."},
			"scale_threshold_id": schema.StringAttribute{Required: true, Validators: id,
				Description: "ID of the library threshold to apply, for example `mtncloud_scale_threshold.busy.id`. " +
					"Changing it copies the new threshold's values in place."},
			// Optional+Computed: a weekly schedule without one is sent UTC; a dated one has none.
			"time_zone": schema.StringAttribute{Optional: true, Computed: true,
				Description: "Time zone of `start_time` and `end_time`, for example `Africa/Lagos`. Defaults to `UTC`. Only with `start_day`."},
			"start_day": schema.StringAttribute{Optional: true, Validators: day,
				Description: "Day the schedule turns on each week, in lower case: `sunday` to `saturday`. Give it with `start_time`, `end_day` and `end_time`, or give `start_date` and `end_date` instead."},
			"start_time": schema.StringAttribute{Optional: true, Validators: clock, Description: "Time on `start_day` the schedule turns on, 24-hour `HH:MM`."},
			"end_day":    schema.StringAttribute{Optional: true, Validators: day, Description: "Day the schedule turns off each week, `sunday` to `saturday`."},
			"end_time":   schema.StringAttribute{Optional: true, Validators: clock, Description: "Time on `end_day` the schedule turns off, 24-hour `HH:MM`."},
			"start_date": schema.StringAttribute{Optional: true,
				Description: "Moment the schedule turns on, once, in RFC 3339 form, for example `2026-12-24T00:00:00Z`. Give it with `end_date`, instead of the weekly days and times."},
			"end_date": schema.StringAttribute{Optional: true, Description: "Moment the schedule turns off, in RFC 3339 form."},
			"threshold": schema.SingleNestedAttribute{Computed: true,
				Description: "The values copied from the library threshold, as MTN Cloud holds them.",
				Attributes: map[string]schema.Attribute{
					"auto_upscale":          schema.BoolAttribute{Computed: true, Description: "Whether a server is added above the max marks."},
					"auto_downscale":        schema.BoolAttribute{Computed: true, Description: "Whether a server is removed below the min marks."},
					"min_count":             schema.Int64Attribute{Computed: true, Description: "Fewest servers."},
					"max_count":             schema.Int64Attribute{Computed: true, Description: "Most servers."},
					"min_cpu_percentage":    float("Low CPU mark"),
					"max_cpu_percentage":    float("High CPU mark"),
					"min_memory_percentage": float("Low memory mark"),
					"max_memory_percentage": float("High memory mark"),
					"min_disk_percentage":   float("Low disk mark"),
					"max_disk_percentage":   float("High disk mark"),
				}},
		},
	}
}

// scaleScheduleID is the resource's ID, "<instance>/<schedule>", and parseScaleScheduleID reads it.
func scaleScheduleID(inst, sched int64) string { return fmt.Sprintf("%d/%d", inst, sched) }

func parseScaleScheduleID(id string) (inst, sched int64, err error) {
	a, b, ok := strings.Cut(id, "/")
	inst, err1 := strconv.ParseInt(a, 10, 64)
	sched, err2 := strconv.ParseInt(b, 10, 64)
	if !ok || err1 != nil || err2 != nil || inst <= 0 || sched <= 0 {
		return 0, 0, fmt.Errorf("%q isn't <instance_id>/<schedule_id>, for example 4650/3", id)
	}
	return inst, sched, nil
}

func (r *scaleScheduleResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var m scaleScheduleModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	weekly := []types.String{m.StartDay, m.StartTime, m.EndDay, m.EndTime}
	dates := []types.String{m.StartDate, m.EndDate}
	set := func(vs []types.String) (n int, unknown bool) {
		for _, v := range vs {
			if v.IsUnknown() {
				unknown = true
			} else if !v.IsNull() {
				n++
			}
		}
		return n, unknown
	}
	w, wu := set(weekly)
	d, du := set(dates)
	if wu || du {
		return
	}
	switch {
	case w > 0 && d > 0:
		resp.Diagnostics.AddError("Weekly times and dates together",
			"Give either start_day, start_time, end_day and end_time (every week), or start_date and end_date (once), not both.")
	case w == 0 && d == 0:
		resp.Diagnostics.AddError("No schedule", "Give start_day, start_time, end_day and end_time (every week), or start_date and end_date (once).")
	case w > 0 && w < 4:
		resp.Diagnostics.AddError("Weekly schedule incomplete", "A weekly schedule needs all of start_day, start_time, end_day and end_time.")
	case d == 1:
		resp.Diagnostics.AddError("Dated schedule incomplete", "A dated schedule needs both start_date and end_date.")
	case d == 2:
		for _, a := range []struct {
			name string
			v    types.String
		}{{"start_date", m.StartDate}, {"end_date", m.EndDate}} {
			if _, err := time.Parse(time.RFC3339, a.v.ValueString()); err != nil {
				resp.Diagnostics.AddAttributeError(path.Root(a.name), "Not an RFC 3339 time",
					fmt.Sprintf("%s = %q: use a form like 2026-12-24T00:00:00Z.", a.name, a.v.ValueString()))
			}
		}
		start, err1 := time.Parse(time.RFC3339, m.StartDate.ValueString())
		end, err2 := time.Parse(time.RFC3339, m.EndDate.ValueString())
		if err1 == nil && err2 == nil && !end.After(start) {
			resp.Diagnostics.AddAttributeError(path.Root("end_date"), "end_date isn't after start_date",
				fmt.Sprintf("The schedule would never be on: %s to %s.", m.StartDate.ValueString(), m.EndDate.ValueString()))
		}
		if !m.TimeZone.IsNull() && !m.TimeZone.IsUnknown() {
			resp.Diagnostics.AddAttributeError(path.Root("time_zone"), "time_zone with dates",
				"time_zone applies to the weekly times only. Put the zone in start_date and end_date instead, for example 2026-12-24T00:00:00+01:00.")
		}
	}
}

// ModifyPlan keeps the copied values while the threshold stays the same, and plans them as unknown
// when it changes (applying copies the new threshold's values).
func (r *scaleScheduleResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.State.Raw.IsNull() || req.Plan.Raw.IsNull() {
		return
	}
	var plan, state scaleScheduleModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if plan.ScaleThresholdID.Equal(state.ScaleThresholdID) {
		resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("threshold"), state.Threshold)...)
	}
}

// spec reads a plan into what the client sends. copyThreshold sends the threshold, which copies its
// values again; without it the schedule keeps the values it has (live QA 2026-10-31).
func (m *scaleScheduleModel) spec(copyThreshold bool) client.ScaleScheduleSpec {
	var th int64
	if copyThreshold {
		th, _ = strconv.ParseInt(m.ScaleThresholdID.ValueString(), 10, 64)
	}
	if !m.StartDate.IsNull() {
		return client.ScaleScheduleSpec{Type: "exact", StartDate: m.StartDate.ValueString(), EndDate: m.EndDate.ValueString(), ThresholdID: th}
	}
	tz := m.TimeZone.ValueString()
	if tz == "" {
		tz = "UTC"
	}
	return client.ScaleScheduleSpec{
		Type: "dayOfWeek", TimeZone: tz,
		StartDay: dayNumber(m.StartDay.ValueString()), EndDay: dayNumber(m.EndDay.ValueString()),
		StartTime: m.StartTime.ValueString(), EndTime: m.EndTime.ValueString(), ThresholdID: th,
	}
}

// sameWhen reports whether an existing schedule covers the same times as a spec, for adopting it.
func sameWhen(s client.ScaleSchedule, want client.ScaleScheduleSpec) bool {
	if want.Type == "exact" {
		return s.Type == "exact" && sameMoment(s.StartDate, want.StartDate) && sameMoment(s.EndDate, want.EndDate)
	}
	return s.Type == "dayOfWeek" && s.StartDay == want.StartDay && s.EndDay == want.EndDay &&
		s.StartTime == want.StartTime && s.EndTime == want.EndTime && strings.EqualFold(s.TimeZone, want.TimeZone)
}

func sameMoment(a, b string) bool {
	ta, err1 := time.Parse(time.RFC3339, a)
	tb, err2 := time.Parse(time.RFC3339, b)
	return err1 == nil && err2 == nil && ta.Equal(tb)
}

func (r *scaleScheduleResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	if !r.ready(&resp.Diagnostics) {
		return
	}
	var m scaleScheduleModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	inst, _ := strconv.ParseInt(m.InstanceID.ValueString(), 10, 64)
	spec := m.spec(true)
	existing, err := r.client.ScaleSchedules(ctx, inst)
	if err != nil {
		resp.Diagnostics.AddAttributeError(path.Root("instance_id"), "Unable to read the instance's scale schedules", err.Error())
		return
	}
	src, err := r.client.ScaleThreshold(ctx, spec.ThresholdID)
	if err != nil {
		resp.Diagnostics.AddAttributeError(path.Root("scale_threshold_id"), "Unable to read scale threshold "+m.ScaleThresholdID.ValueString(), err.Error())
		return
	}
	var s *client.ScaleSchedule
	for i := range existing {
		// A retry after a create that did happen (rule 11): the same times, zone and copied values.
		// Schedules have no name, so one that differs in any of them is someone else's and is left alone.
		if sameWhen(existing[i], spec) && sameThreshold(existing[i].Threshold, *src) {
			tflog.Info(ctx, "adopting an existing scale schedule with the same times and threshold",
				map[string]any{"instance": inst, "schedule": existing[i].ID})
			s = &existing[i]
			break
		}
	}
	if s == nil && err == nil {
		s, err = r.client.CreateScaleSchedule(ctx, inst, spec)
	}
	if err != nil {
		resp.Diagnostics.AddError("Unable to add the scale schedule to instance "+m.InstanceID.ValueString(), err.Error())
		return
	}
	m.ID = types.StringValue(scaleScheduleID(inst, s.ID))
	resp.Diagnostics.Append(m.fill(s)...)
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

func (r *scaleScheduleResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	if !r.ready(&resp.Diagnostics) {
		return
	}
	var m scaleScheduleModel
	resp.Diagnostics.Append(req.State.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	inst, sid, err := parseScaleScheduleID(m.ID.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Invalid ID", err.Error())
		return
	}
	s, err := r.client.ScaleSchedule(ctx, inst, sid)
	if client.IsNotFound(err) {
		resp.State.RemoveResource(ctx) // the schedule or its instance is gone: plan making it again
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Unable to read scale schedule "+m.ID.ValueString(), err.Error())
		return
	}
	m.InstanceID = types.StringValue(strconv.FormatInt(inst, 10))
	resp.Diagnostics.Append(m.fill(s)...)
	// The schedule holds a copy: when the library threshold has changed since (or is gone), forget it
	// so the plan sets it again and applying copies the new values.
	if th, err := strconv.ParseInt(m.ScaleThresholdID.ValueString(), 10, 64); err == nil {
		src, err := r.client.ScaleThreshold(ctx, th)
		switch {
		case client.IsNotFound(err):
			m.ScaleThresholdID = types.StringNull()
		case err != nil:
			resp.Diagnostics.AddWarning("Unable to compare the schedule with its threshold",
				fmt.Sprintf("Reading scale threshold %d: %s. The schedule keeps the values it has.", th, err))
		case !sameThreshold(*src, s.Threshold):
			m.ScaleThresholdID = types.StringNull()
		}
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

// sameThreshold compares the values a schedule copies.
func sameThreshold(a, b client.Threshold) bool {
	sameRange := func(x, y *client.Range) bool {
		return (x == nil) == (y == nil) && (x == nil || *x == *y)
	}
	return a.AutoUp == b.AutoUp && a.AutoDown == b.AutoDown && a.MinCount == b.MinCount && a.MaxCount == b.MaxCount &&
		a.Increment == b.Increment &&
		sameRange(a.CPU, b.CPU) && sameRange(a.Memory, b.Memory) && sameRange(a.Disk, b.Disk)
}

// fill sets the model from a schedule the API returned. A date or time that means the same moment as
// the configured one keeps the configured spelling.
func (m *scaleScheduleModel) fill(s *client.ScaleSchedule) diag.Diagnostics {
	if s.Type == "exact" {
		m.StartDay, m.StartTime, m.EndDay, m.EndTime = types.StringNull(), types.StringNull(), types.StringNull(), types.StringNull()
		if !sameMoment(m.StartDate.ValueString(), s.StartDate) {
			m.StartDate = types.StringValue(s.StartDate)
		}
		if !sameMoment(m.EndDate.ValueString(), s.EndDate) {
			m.EndDate = types.StringValue(s.EndDate)
		}
		m.TimeZone = types.StringNull() // a dated schedule has no zone of its own
	} else {
		m.StartDate, m.EndDate = types.StringNull(), types.StringNull()
		m.StartDay, m.EndDay = types.StringValue(dayName(s.StartDay)), types.StringValue(dayName(s.EndDay))
		m.StartTime, m.EndTime = types.StringValue(s.StartTime), types.StringValue(s.EndTime)
		if !strings.EqualFold(m.TimeZone.ValueString(), s.TimeZone) || m.TimeZone.IsUnknown() || m.TimeZone.IsNull() {
			m.TimeZone = types.StringValue(s.TimeZone)
		}
	}
	t := s.Threshold
	f := func(r *client.Range, hi bool) types.Float64 {
		if r == nil {
			return types.Float64Null()
		}
		if hi {
			return types.Float64Value(r.Max)
		}
		return types.Float64Value(r.Min)
	}
	obj, d := types.ObjectValue(copiedThresholdTypes, map[string]attr.Value{
		"auto_upscale": types.BoolValue(t.AutoUp), "auto_downscale": types.BoolValue(t.AutoDown),
		"min_count": types.Int64Value(t.MinCount), "max_count": types.Int64Value(t.MaxCount),
		"min_cpu_percentage": f(t.CPU, false), "max_cpu_percentage": f(t.CPU, true),
		"min_memory_percentage": f(t.Memory, false), "max_memory_percentage": f(t.Memory, true),
		"min_disk_percentage": f(t.Disk, false), "max_disk_percentage": f(t.Disk, true),
	})
	m.Threshold = obj
	return d
}

func (r *scaleScheduleResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	if !r.ready(&resp.Diagnostics) {
		return
	}
	var m, state scaleScheduleModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &m)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	inst, sid, err := parseScaleScheduleID(m.ID.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Invalid ID", err.Error())
		return
	}
	// Copy the threshold only when it changed, or when Read found the copy out of date (it then
	// dropped the ID). Otherwise the plan keeps the copied values, and copying again would break that
	// promise when the library threshold changes in the same apply.
	copyThreshold := !m.ScaleThresholdID.Equal(state.ScaleThresholdID)
	s, err := r.client.UpdateScaleSchedule(ctx, inst, sid, m.spec(copyThreshold))
	if err != nil {
		resp.Diagnostics.AddError("Unable to update scale schedule "+m.ID.ValueString(), err.Error())
		return
	}
	resp.Diagnostics.Append(m.fill(s)...)
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

func (r *scaleScheduleResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	if !r.ready(&resp.Diagnostics) {
		return
	}
	var m scaleScheduleModel
	resp.Diagnostics.Append(req.State.Get(ctx, &m)...)
	inst, sid, err := parseScaleScheduleID(m.ID.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Invalid ID", err.Error())
		return
	}
	if err := r.client.DeleteScaleSchedule(ctx, inst, sid); err != nil && !client.IsNotFound(err) {
		resp.Diagnostics.AddError("Unable to remove scale schedule "+m.ID.ValueString(), err.Error())
	}
}

// ImportState takes "<instance_id>/<schedule_id>". The schedule doesn't say which threshold it was
// copied from, so the first plan after an import sets scale_threshold_id, and applying copies it in.
func (r *scaleScheduleResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	if _, _, err := parseScaleScheduleID(req.ID); err != nil {
		resp.Diagnostics.AddError("Invalid import ID", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("threshold"), types.ObjectNull(copiedThresholdTypes))...)
}
