package compute

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
	_ "time/tzdata" // time zone names are checked at plan time on every OS

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-framework/types/basetypes"

	"github.com/mtn/terraform-provider-mtncloud/internal/client"
)

// powerScheduleResource is a weekly plan of when instances run (/api/power-schedules). Instances
// join it through mtncloud_instance_power_schedule, so the schedule and its members don't fight.
type powerScheduleResource struct{ base }

func newPowerSchedule() resource.Resource { return &powerScheduleResource{} }

var (
	_ resource.ResourceWithImportState    = (*powerScheduleResource)(nil)
	_ resource.ResourceWithValidateConfig = (*powerScheduleResource)(nil)
)

type dayModel struct {
	On  types.String `tfsdk:"on"`
	Off types.String `tfsdk:"off"`
}

type powerScheduleModel struct {
	ID          types.String  `tfsdk:"id"`
	Name        types.String  `tfsdk:"name"`
	Description types.String  `tfsdk:"description"`
	Enabled     types.Bool    `tfsdk:"enabled"`
	StopOnly    types.Bool    `tfsdk:"stop_only"`
	TimeZone    types.String  `tfsdk:"time_zone"`
	Monday      *dayModel     `tfsdk:"monday"`
	Tuesday     *dayModel     `tfsdk:"tuesday"`
	Wednesday   *dayModel     `tfsdk:"wednesday"`
	Thursday    *dayModel     `tfsdk:"thursday"`
	Friday      *dayModel     `tfsdk:"friday"`
	Saturday    *dayModel     `tfsdk:"saturday"`
	Sunday      *dayModel     `tfsdk:"sunday"`
	HoursSaved  types.Float64 `tfsdk:"monthly_hours_saved"`
}

// days gives the day fields in client.Weekdays order, so loops can set them.
func (m *powerScheduleModel) days() []**dayModel {
	return []**dayModel{&m.Monday, &m.Tuesday, &m.Wednesday, &m.Thursday, &m.Friday, &m.Saturday, &m.Sunday}
}

// onTime is a 24-hour "HH:MM"; an off time may also be "24:00", the end of the day.
var (
	onTime  = regexp.MustCompile(`^([01][0-9]|2[0-3]):[0-5][0-9]$`)
	offTime = regexp.MustCompile(`^(([01][0-9]|2[0-3]):[0-5][0-9]|24:00)$`)
)

func (r *powerScheduleResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_power_schedule"
}

func (r *powerScheduleResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	day := func(name string) schema.SingleNestedAttribute {
		return schema.SingleNestedAttribute{Optional: true,
			Description: "Hours instances run on " + name + ". Leave it out to run all day. `{ on = \"00:00\", off = \"00:00\" }` keeps them off all day.",
			Attributes: map[string]schema.Attribute{
				"on": schema.StringAttribute{Required: true,
					Validators:  []validator.String{stringvalidator.RegexMatches(onTime, `must be a 24-hour time, for example "07:00"`)},
					Description: "Time instances start, 24-hour `HH:MM`."},
				"off": schema.StringAttribute{Required: true,
					Validators:  []validator.String{stringvalidator.RegexMatches(offTime, `must be a 24-hour time, for example "19:00", or "24:00"`)},
					Description: "Time instances stop, 24-hour `HH:MM`, not before `on`. `24:00` is the end of the day."},
			}}
	}
	attrs := map[string]schema.Attribute{
		"id": schema.StringAttribute{Computed: true, PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			Description: "ID of the power schedule."},
		"name": schema.StringAttribute{Required: true, Validators: []validator.String{stringvalidator.LengthAtLeast(1)}, Description: "Name of the schedule."},
		"description": schema.StringAttribute{Optional: true, Validators: []validator.String{stringvalidator.LengthAtLeast(1)},
			Description: "Description of the schedule. Leave it out for none."},
		// Optional+Computed with defaults: the API's own defaults, so leaving them out is a clean plan.
		"enabled": schema.BoolAttribute{Optional: true, Computed: true, Default: booldefault.StaticBool(true),
			Description: "Whether the schedule starts and stops its instances. Defaults to `true`."},
		"stop_only": schema.BoolAttribute{Optional: true, Computed: true, Default: booldefault.StaticBool(false),
			Description: "Only stop instances at the off times, never start them. Defaults to `false` (start and stop)."},
		"time_zone": schema.StringAttribute{Optional: true, Computed: true, Default: stringdefault.StaticString("UTC"),
			Validators:  []validator.String{timeZoneName{}},
			Description: "Time zone of the times, an IANA name such as `Africa/Lagos`. Defaults to `UTC`."},
		"monthly_hours_saved": schema.Float64Attribute{Computed: true,
			Description: "Hours a month an instance on this schedule is off, as MTN Cloud counts them."},
	}
	for _, d := range client.Weekdays {
		attrs[d] = day(d)
	}
	resp.Schema = schema.Schema{
		Description: "A weekly power schedule: the hours each day that instances on it run. MTN Cloud stops them at " +
			"the off time and starts them at the on time. Put instances on it with `mtncloud_instance_power_schedule`. " +
			"A schedule an instance is still on can't be deleted, so destroy the attachments first (Terraform does when " +
			"they reference the schedule).",
		Attributes: attrs,
	}
}

// validDay checks one day's times beyond their format, as MTN Cloud does (live QA 2026-10-31).
func validDay(name string, d dayModel) string {
	if d.On.IsUnknown() || d.Off.IsUnknown() || d.On.IsNull() || d.Off.IsNull() {
		return ""
	}
	on, off := d.On.ValueString(), d.Off.ValueString()
	switch {
	case on == "00:00" && off == "24:00":
		return fmt.Sprintf("%s runs all day: leave %s out instead (that is the default).", name, name)
	case off < on:
		return fmt.Sprintf("%s: off (%s) is before on (%s). MTN Cloud refuses it.", name, off, on)
	case off == on && on != "00:00":
		return fmt.Sprintf(`%s: on and off are both %s. To keep instances off all day, give { on = "00:00", off = "00:00" }.`, name, on)
	}
	return ""
}

func (r *powerScheduleResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	// Each day is read on its own as an object: a day known only after apply (from a variable or another
	// resource) is skipped here, and checked by MTN Cloud when it is sent.
	for _, name := range client.Weekdays {
		var o types.Object
		resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root(name), &o)...)
		if o.IsNull() || o.IsUnknown() {
			continue
		}
		var d dayModel
		resp.Diagnostics.Append(o.As(ctx, &d, basetypes.ObjectAsOptions{})...)
		if msg := validDay(name, d); msg != "" {
			resp.Diagnostics.AddAttributeError(path.Root(name), "Invalid hours", msg)
		}
	}
}

// timeZoneName checks a time zone is an IANA name: MTN Cloud stores whatever it is sent.
type timeZoneName struct{}

func (timeZoneName) Description(context.Context) string { return "must be an IANA time zone name" }

func (v timeZoneName) MarkdownDescription(ctx context.Context) string { return v.Description(ctx) }

func (timeZoneName) ValidateString(_ context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	if _, err := time.LoadLocation(req.ConfigValue.ValueString()); err != nil || req.ConfigValue.ValueString() == "" {
		resp.Diagnostics.AddAttributeError(req.Path, "Unknown time zone",
			fmt.Sprintf(`%q isn't a time zone name. Use an IANA name such as "Africa/Lagos" or "UTC" (case matters).`, req.ConfigValue.ValueString()))
	}
}

func (m *powerScheduleModel) spec() client.PowerSchedule {
	s := client.PowerSchedule{
		Name: m.Name.ValueString(), Description: m.Description.ValueString(),
		Enabled: m.Enabled.ValueBool(), StopOnly: m.StopOnly.ValueBool(), TimeZone: m.TimeZone.ValueString(),
		Days: map[string]client.DayHours{},
	}
	for i, d := range m.days() {
		if *d != nil {
			s.Days[client.Weekdays[i]] = client.DayHours{On: (*d).On.ValueString(), Off: (*d).Off.ValueString()}
		}
	}
	return s
}

// fill copies what MTN Cloud holds into the model. A day on all day is null (left out), the
// description "" is null, and a time zone that differs only in case keeps the config's spelling.
func (m *powerScheduleModel) fill(s *client.PowerSchedule) {
	m.ID = types.StringValue(strconv.FormatInt(s.ID, 10))
	m.Name = types.StringValue(s.Name)
	m.Description = types.StringNull()
	if s.Description != "" {
		m.Description = types.StringValue(s.Description)
	}
	m.Enabled = types.BoolValue(s.Enabled)
	m.StopOnly = types.BoolValue(s.StopOnly)
	tz := s.TimeZone
	if tz == "" {
		tz = "UTC" // what MTN Cloud uses when it holds none
	}
	if !strings.EqualFold(m.TimeZone.ValueString(), tz) || m.TimeZone.IsNull() || m.TimeZone.IsUnknown() {
		m.TimeZone = types.StringValue(tz)
	}
	for i, d := range m.days() {
		h := s.Days[client.Weekdays[i]]
		if h == client.AllDay || h == (client.DayHours{}) {
			*d = nil
			continue
		}
		*d = &dayModel{On: types.StringValue(h.On), Off: types.StringValue(h.Off)}
	}
	m.HoursSaved = types.Float64Value(s.HoursSaved)
}

func (r *powerScheduleResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	if !r.ready(&resp.Diagnostics) {
		return
	}
	var m powerScheduleModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	want := m.spec()
	// Names aren't unique and a schedule records nothing about who made it, so one with this name is
	// never taken over (it may be another config's): the create stops and says to import it. That also
	// covers a create retried after a timeout.
	existing, err := r.client.PowerSchedulesNamed(ctx, want.Name)
	if err != nil {
		resp.Diagnostics.AddError("Unable to look for power schedule "+want.Name, err.Error())
		return
	}
	for _, e := range existing {
		if e.Name == want.Name {
			resp.Diagnostics.AddAttributeError(path.Root("name"), "Power schedule already exists",
				fmt.Sprintf("A power schedule named %q already exists (ID %d). Import it to manage it here "+
					"(terraform import <address> %d), or give this one another name.", want.Name, e.ID, e.ID))
			return
		}
	}
	s, err := r.client.CreatePowerSchedule(ctx, want)
	if err != nil {
		resp.Diagnostics.AddError("Unable to create power schedule "+want.Name, err.Error())
		return
	}
	m.fill(s)
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

func (r *powerScheduleResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	if !r.ready(&resp.Diagnostics) {
		return
	}
	var m powerScheduleModel
	resp.Diagnostics.Append(req.State.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	id, err := strconv.ParseInt(m.ID.ValueString(), 10, 64)
	if err != nil {
		resp.Diagnostics.AddError("Invalid ID", fmt.Sprintf("%q isn't a power schedule ID.", m.ID.ValueString()))
		return
	}
	s, err := r.client.PowerSchedule(ctx, id)
	if client.IsNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Unable to read power schedule "+m.ID.ValueString(), err.Error())
		return
	}
	m.fill(s)
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

func (r *powerScheduleResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	if !r.ready(&resp.Diagnostics) {
		return
	}
	var m, state powerScheduleModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &m)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	id, err := strconv.ParseInt(state.ID.ValueString(), 10, 64)
	if err != nil {
		resp.Diagnostics.AddError("Invalid ID", fmt.Sprintf("%q isn't a power schedule ID.", state.ID.ValueString()))
		return
	}
	s, err := r.client.UpdatePowerSchedule(ctx, id, m.spec())
	if err != nil {
		resp.Diagnostics.AddError("Unable to update power schedule "+state.ID.ValueString(), err.Error())
		return
	}
	m.fill(s)
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

func (r *powerScheduleResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	if !r.ready(&resp.Diagnostics) {
		return
	}
	var m powerScheduleModel
	resp.Diagnostics.Append(req.State.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	id, err := strconv.ParseInt(m.ID.ValueString(), 10, 64)
	if err != nil {
		resp.Diagnostics.AddError("Invalid ID", fmt.Sprintf("%q isn't a power schedule ID.", m.ID.ValueString()))
		return
	}
	if err := r.client.DeletePowerSchedule(ctx, id); err != nil {
		resp.Diagnostics.AddError("Unable to delete power schedule "+m.ID.ValueString(),
			err.Error()+"\n\nA schedule an instance is still on can't be deleted: remove its "+
				"mtncloud_instance_power_schedule first, or take the instance off it in the console.")
	}
}

func (r *powerScheduleResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	if !digits.MatchString(req.ID) {
		resp.Diagnostics.AddError("Invalid import ID", fmt.Sprintf("%q isn't a power schedule ID (a number).", req.ID))
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
}

// instancePowerScheduleResource puts one instance on a power schedule. Its ID is the instance's.
type instancePowerScheduleResource struct{ base }

func newInstancePowerSchedule() resource.Resource { return &instancePowerScheduleResource{} }

var _ resource.ResourceWithImportState = (*instancePowerScheduleResource)(nil)

type instancePowerScheduleModel struct {
	ID              types.String `tfsdk:"id"`
	InstanceID      types.String `tfsdk:"instance_id"`
	PowerScheduleID types.String `tfsdk:"power_schedule_id"`
}

func (r *instancePowerScheduleResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_instance_power_schedule"
}

func (r *instancePowerScheduleResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	id := []validator.String{stringvalidator.RegexMatches(digits, "must be an ID")}
	resp.Schema = schema.Schema{
		Description: "Puts an instance on a power schedule (`mtncloud_power_schedule`): MTN Cloud then stops and starts " +
			"it at the schedule's times. An instance is on one schedule at most; this replaces any it was on. " +
			"Destroying this takes the instance off the schedule and leaves it in its current power state. " +
			"Don't also set `power_state` on the instance's `mtncloud_instance`, or the two undo each other.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{Computed: true, PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
				Description: "The instance's ID."},
			"instance_id": schema.StringAttribute{Required: true, Validators: id,
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
				Description:   "ID of the instance, for example `mtncloud_instance.web.id`. Changing it moves the attachment to another instance."},
			"power_schedule_id": schema.StringAttribute{Required: true, Validators: id,
				Description: "ID of the schedule, for example `mtncloud_power_schedule.office_hours.id`. Changing it moves the instance in place."},
		},
	}
}

func parseIDs(m instancePowerScheduleModel) (inst, sched int64, err error) {
	if inst, err = strconv.ParseInt(m.InstanceID.ValueString(), 10, 64); err != nil {
		return 0, 0, fmt.Errorf("%q isn't an instance ID", m.InstanceID.ValueString())
	}
	sched, _ = strconv.ParseInt(m.PowerScheduleID.ValueString(), 10, 64)
	return inst, sched, nil
}

// setKey is the private state key holding the schedule this resource last set, so Delete undoes only
// its own: Read copies whatever schedule the instance is on into power_schedule_id.
const setKey = "power_schedule_set"

func (r *instancePowerScheduleResource) set(ctx context.Context, m *instancePowerScheduleModel) error {
	inst, sched, err := parseIDs(*m)
	if err != nil {
		return err
	}
	if err := r.client.SetInstancePowerSchedule(ctx, inst, sched); err != nil {
		return err
	}
	m.ID = types.StringValue(strconv.FormatInt(inst, 10))
	return nil
}

func (r *instancePowerScheduleResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	if !r.ready(&resp.Diagnostics) {
		return
	}
	var m instancePowerScheduleModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.set(ctx, &m); err != nil {
		resp.Diagnostics.AddError("Unable to put instance "+m.InstanceID.ValueString()+" on power schedule "+m.PowerScheduleID.ValueString(), err.Error())
		return
	}
	resp.Diagnostics.Append(resp.Private.SetKey(ctx, setKey, []byte(m.PowerScheduleID.ValueString()))...)
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

func (r *instancePowerScheduleResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	if !r.ready(&resp.Diagnostics) {
		return
	}
	var m instancePowerScheduleModel
	resp.Diagnostics.Append(req.State.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	inst, err := strconv.ParseInt(m.ID.ValueString(), 10, 64)
	if err != nil {
		resp.Diagnostics.AddError("Invalid ID", fmt.Sprintf("%q isn't an instance ID.", m.ID.ValueString()))
		return
	}
	sched, err := r.client.InstancePowerSchedule(ctx, inst)
	if client.IsNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Unable to read instance "+m.ID.ValueString(), err.Error())
		return
	}
	if sched == 0 {
		resp.State.RemoveResource(ctx) // taken off its schedule out of band: plan putting it back
		return
	}
	m.InstanceID = types.StringValue(strconv.FormatInt(inst, 10))
	m.PowerScheduleID = types.StringValue(strconv.FormatInt(sched, 10))
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

func (r *instancePowerScheduleResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	if !r.ready(&resp.Diagnostics) {
		return
	}
	var m instancePowerScheduleModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.set(ctx, &m); err != nil {
		resp.Diagnostics.AddError("Unable to move instance "+m.InstanceID.ValueString()+" to power schedule "+m.PowerScheduleID.ValueString(), err.Error())
		return
	}
	resp.Diagnostics.Append(resp.Private.SetKey(ctx, setKey, []byte(m.PowerScheduleID.ValueString()))...)
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

func (r *instancePowerScheduleResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	if !r.ready(&resp.Diagnostics) {
		return
	}
	var m instancePowerScheduleModel
	resp.Diagnostics.Append(req.State.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	inst, sched, err := parseIDs(m)
	if err != nil {
		resp.Diagnostics.AddError("Invalid ID", err.Error())
		return
	}
	// The schedule this resource set. An imported one has none, and owns the schedule it imported.
	if b, d := req.Private.GetKey(ctx, setKey); !d.HasError() && len(b) > 0 {
		if v, err := strconv.ParseInt(string(b), 10, 64); err == nil {
			sched = v
		}
	}
	cur, err := r.client.InstancePowerSchedule(ctx, inst)
	if client.IsNotFound(err) {
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Unable to read instance "+m.InstanceID.ValueString(), err.Error())
		return
	}
	if cur != sched {
		return // on another schedule now (set elsewhere): not this resource's to undo
	}
	if err := r.client.SetInstancePowerSchedule(ctx, inst, 0); err != nil && !client.IsNotFound(err) {
		resp.Diagnostics.AddError("Unable to take instance "+m.InstanceID.ValueString()+" off its power schedule", err.Error())
	}
}

func (r *instancePowerScheduleResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	if !digits.MatchString(req.ID) {
		resp.Diagnostics.AddError("Invalid import ID", fmt.Sprintf("%q isn't an instance ID (a number).", req.ID))
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
}
