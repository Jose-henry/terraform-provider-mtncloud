package compute

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-timeouts/resource/timeouts"
	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/mapplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/setplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/mtn/terraform-provider-mtncloud/internal/client"
)

// instanceSettingsResource manages the day-2 settings of an instance something else owns: a catalog
// order's VM, an app's, or one made in the console (B-028). It never creates or deletes the VM.
type instanceSettingsResource struct{ base }

func newInstanceSettings() resource.Resource { return &instanceSettingsResource{} }

var (
	_ resource.ResourceWithConfigure      = (*instanceSettingsResource)(nil)
	_ resource.ResourceWithImportState    = (*instanceSettingsResource)(nil)
	_ resource.ResourceWithValidateConfig = (*instanceSettingsResource)(nil)
	_ resource.ResourceWithModifyPlan     = (*instanceSettingsResource)(nil)
)

type instanceSettingsModel struct {
	ID             types.String   `tfsdk:"id"`
	InstanceID     types.String   `tfsdk:"instance_id"`
	Name           types.String   `tfsdk:"name"`
	Group          types.String   `tfsdk:"group"`
	Image          types.String   `tfsdk:"image"`
	Description    types.String   `tfsdk:"description"`
	Plan           types.String   `tfsdk:"plan"`
	SecurityGroups types.Set      `tfsdk:"security_groups"`
	Labels         types.Set      `tfsdk:"labels"`
	Tags           types.Map      `tfsdk:"tags"`
	PowerState     types.String   `tfsdk:"power_state"`
	RestartTrigger types.String   `tfsdk:"restart_trigger"`
	Status         types.String   `tfsdk:"status"`
	PrivateIP      types.String   `tfsdk:"private_ip"`
	PublicIP       types.String   `tfsdk:"public_ip"`
	PublicIPs      types.List     `tfsdk:"public_ips"`
	FloatingIPPool types.String   `tfsdk:"floating_ip_pool"`
	Scale          types.Object   `tfsdk:"scale"`
	ExpireDate     types.String   `tfsdk:"expire_date"`
	DataVolumes    types.List     `tfsdk:"data_volume"`
	Timeouts       timeouts.Value `tfsdk:"timeouts"`
}

func (r *instanceSettingsResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_instance_settings"
}

func (r *instanceSettingsResource) Schema(ctx context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	keep := []planmodifier.String{stringplanmodifier.UseStateForUnknown()}
	// Every setting is Optional+Computed: left out, it isn't managed and shows the VM's own value;
	// set, the VM is kept at it. Removing one stops managing it and leaves the VM as it is.
	resp.Schema = schema.Schema{
		Description: "Day-2 settings of an instance this configuration doesn't own: a catalog order's VM, a managed " +
			"service's or app's, or one made in the console. Create adopts the instance by `instance_id` and changes " +
			"only the settings you set; settings you leave out aren't managed and show the VM's own values. **Destroy " +
			"leaves the VM**: it only stops managing these settings (delete the VM through whatever owns it). An instance " +
			"with several servers takes each setting on every server. The VM's image, project, networks, root disk and " +
			"name can't change here: changing them would rebuild it. Manage an instance with either `mtncloud_instance` " +
			"or this resource, never both.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{Computed: true, PlanModifiers: keep, Description: "The instance's ID."},
			"instance_id": schema.StringAttribute{Required: true,
				Description:   "ID of the instance, for example a catalog order's `instance_ids[0]`. Changing it adopts another instance.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
				Validators:    []validator.String{stringvalidator.RegexMatches(digits, "must be an instance ID")}},
			"name":  schema.StringAttribute{Computed: true, Description: "The instance's name, read only."},
			"group": schema.StringAttribute{Computed: true, PlanModifiers: keep, Description: "The instance's group (availability zone), read only."},
			"image": schema.StringAttribute{Computed: true, PlanModifiers: keep, Description: "The instance's image, read only."},
			"description": schema.StringAttribute{Optional: true, Computed: true,
				Description: "Description shown in the console. Changed in place."},
			"plan": schema.StringAttribute{Optional: true, Computed: true,
				Description: "Plan (size) by name, for example `G2S4`. Changing it resizes the instance in place and **restarts " +
					"it**; the root disk keeps its size. Left out, the plan isn't managed."},
			"security_groups": schema.SetAttribute{Optional: true, Computed: true, ElementType: types.StringType,
				Description: "Security groups by name, in the instance's project, in addition to the project's default group, " +
					"which is always applied. Set, they replace the instance's other groups in place.",
				PlanModifiers: []planmodifier.Set{setplanmodifier.UseStateForUnknown()}},
			"labels": schema.SetAttribute{Optional: true, Computed: true, ElementType: types.StringType,
				Description:   "Labels. Set, they replace the instance's labels in place; `[]` clears them.",
				PlanModifiers: []planmodifier.Set{setplanmodifier.UseStateForUnknown()}},
			"tags": schema.MapAttribute{Optional: true, Computed: true, ElementType: types.StringType,
				Description:   "Tags. Set, they replace the instance's tags in place; `{}` clears them.",
				PlanModifiers: []planmodifier.Map{mapplanmodifier.UseStateForUnknown()}},
			"power_state": schema.StringAttribute{Optional: true, Computed: true,
				Description: "`running`, `stopped` or `suspended`. Set, the VM is powered to it in place, and put back by the " +
					"next apply when changed in the console. A plan change restarts the VM, then it is put back in this state. " + suspendNote,
				Validators: []validator.String{stringvalidator.OneOf(powerStates...)}},
			"restart_trigger": schema.StringAttribute{Optional: true,
				Description: "Any value. Changing it restarts a running VM in place, for example after a config change. " +
					"Setting it the first time (at create, after an import, or added later) doesn't restart."},
			"status":     schema.StringAttribute{Computed: true, Description: "The instance's status, for example `running`."},
			"private_ip": schema.StringAttribute{Computed: true, Description: "The instance's private address."},
			"public_ip":  schema.StringAttribute{Computed: true, Description: "The instance's floating IP, when it has one."},
			"public_ips": schema.ListAttribute{Computed: true, ElementType: types.StringType, Description: publicIPsDescription},
			// Optional, not Computed: a customer can't read which pool an address came from, so it is kept
			// as configured while the instance has an address.
			"floating_ip_pool": schema.StringAttribute{Optional: true,
				Description: "Give the instance a floating (public) IP from this pool, for example `public-network-02`, one per " +
					"server. An address it already has is kept. Changing the pool releases the old addresses and attaches new " +
					"ones; an address released in the console is attached again. Left out, floating IPs aren't managed (removing " +
					"it keeps the address). Needs the instance's network to be on an MTN Router with an external network."},
			"scale": scaleAttribute("Automatic scaling: MTN Cloud adds a VM when usage stays above a metric's `max` and " +
				"removes one below its `min`, between `min_count` and `max_count`. Watch at least one of `cpu`, `memory`, `disk`. " +
				"Changed in place, and put back when changed in the console. Left out, scaling isn't managed (and isn't shown); removing it leaves scaling as it is."),
			"expire_date": schema.StringAttribute{Computed: true,
				Description: "When the instance will be deleted, if it expires (set when it was made, or by a policy). Read only."},
		},
		Blocks: map[string]schema.Block{
			"data_volume": schema.ListNestedBlock{
				Description: "The instance's data disks (not the root disk), in order. With none, disks aren't managed. With " +
					"some, they must start with the disks the instance has (the `mtncloud_instance` data source lists them), " +
					"and disks added at the end are attached in place. A disk can't be changed or removed here.",
				NestedObject: schema.NestedBlockObject{Attributes: map[string]schema.Attribute{
					// Optional+Computed: MTN Cloud names unnamed disks.
					"name": schema.StringAttribute{Optional: true, Computed: true, Description: "Disk name.",
						PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
					"size": schema.Int64Attribute{Required: true, Description: "Size in GB.",
						Validators: []validator.Int64{int64validator.AtLeast(1)}},
					"datastore": schema.StringAttribute{Optional: true, Description: "Datastore, by name. Left out, the project's default."},
				}},
			},
			"timeouts": timeouts.Block(ctx, timeouts.Opts{Create: true, Update: true}),
		},
	}
}

func (r *instanceSettingsResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan instanceSettingsModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	timeout, d := plan.Timeouts.Create(ctx, instanceTimeout)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() || !r.ready(&resp.Diagnostics) {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	id, _ := strconv.ParseInt(plan.InstanceID.ValueString(), 10, 64)
	inst, err := r.client.GetInstance(ctx, id)
	if err != nil {
		resp.Diagnostics.AddAttributeError(path.Root("instance_id"), "Unable to read instance "+plan.InstanceID.ValueString(),
			err.Error()+". Check the ID: the instance must exist and be visible to this login.")
		return
	}
	plan.ID = plan.InstanceID
	// What the VM has now is the prior state: only the settings the plan differs on are sent.
	var current instanceSettingsModel
	current.ID, current.Timeouts = plan.ID, plan.Timeouts
	r.refresh(ctx, &current, inst, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	unconfirmed := r.apply(ctx, &plan, current, inst, false, types.StringNull(), &resp.Diagnostics)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
	if unconfirmed {
		recordUnconfirmed(ctx, resp.Private, &resp.Diagnostics)
	}
}

func (r *instanceSettingsResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	if !r.ready(&resp.Diagnostics) {
		return
	}
	var state instanceSettingsModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	id, err := strconv.ParseInt(state.ID.ValueString(), 10, 64)
	if err != nil {
		resp.Diagnostics.AddError("Invalid instance ID", fmt.Sprintf("%q is not an instance ID: %s", state.ID.ValueString(), err))
		return
	}
	inst, err := r.client.GetInstance(ctx, id)
	if client.IsNotFound(err) || (err == nil && strings.EqualFold(inst.Status, "pendingRemoval")) {
		// The VM is gone with whatever owned it: nothing to manage (rule 8).
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Unable to read instance", err.Error())
		return
	}
	r.refresh(ctx, &state, inst, &resp.Diagnostics)
	// Every attach waits for its address, so a missing one was released in the console and the plan
	// attaches one again; except an attach that was sent but never seen, which may yet arrive.
	pending, d := req.Private.GetKey(ctx, fipUnconfirmedKey)
	resp.Diagnostics.Append(d...)
	switch {
	case !inst.MissingPublicIP() && pending != nil:
		resp.Diagnostics.Append(resp.Private.SetKey(ctx, fipUnconfirmedKey, nil)...)
	case inst.MissingPublicIP() && pending != nil && !state.FloatingIPPool.IsNull():
		resp.Diagnostics.AddAttributeWarning(path.Root("floating_ip_pool"), "Floating IP not attached yet",
			fmt.Sprintf("A floating IP from %s was asked for but hasn't shown on instance %s. It isn't asked for again, "+
				"so no second address is attached. If it never appears, remove floating_ip_pool, apply, then add it back.",
				state.FloatingIPPool.ValueString(), state.ID.ValueString()))
	case inst.MissingPublicIP() && !state.FloatingIPPool.IsNull():
		// Released in the console (on any server): attached again. With others still on the pool, it
		// is kept aside, so a pool change in that apply moves them too.
		if !inst.NoPublicIP() {
			rememberPartial(ctx, resp.Private, state.FloatingIPPool, &resp.Diagnostics)
		}
		state.FloatingIPPool = types.StringNull()
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// ModifyPlan refuses, at plan, a disk change the cloud can't make in place: only disks added at the
// end attach in place.
func (r *instanceSettingsResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.Plan.Raw.IsNull() {
		return
	}
	var plan, state instanceSettingsModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if !req.State.Raw.IsNull() {
		resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	}
	if plan.DataVolumes.IsUnknown() || resp.Diagnostics.HasError() {
		return
	}
	pv, sv := volumesOf(ctx, plan.DataVolumes, &resp.Diagnostics), volumesOf(ctx, state.DataVolumes, &resp.Diagnostics)
	// The framework leaves an added disk's name null, but MTN Cloud names it: unknown. A disk the
	// instance already has keeps its name from state (UseStateForUnknown).
	for i := len(sv); i < len(pv); i++ {
		if pv[i].Name.IsNull() {
			at := path.Root("data_volume").AtListIndex(i).AtName("name")
			resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, at, types.StringUnknown())...)
		}
	}
	if len(pv) == 0 || req.State.Raw.IsNull() || len(sv) == 0 {
		return // not managed, or adopting / starting to manage them: checked against the instance at apply
	}
	if msg := disksAppendOnly(pv, sv); msg != "" {
		resp.Diagnostics.AddAttributeError(path.Root("data_volume"), "Disk can't change in place", msg)
	}
}

// disksAppendOnly says why planned disks aren't the current ones plus some at the end ("" when they are).
func disksAppendOnly(plan, current []dataVolumeModel) string {
	if len(plan) < len(current) {
		return fmt.Sprintf("The instance has %d data disks and %d are configured. A disk can't be removed here: list every "+
			"disk it has (%s), then any new ones.", len(current), len(plan), diskList(current))
	}
	for i, c := range current {
		p := plan[i]
		if !p.Size.Equal(c.Size) || (known(p.Name) && p.Name.ValueString() != c.Name.ValueString()) {
			return fmt.Sprintf("Data disk %d is %s, but it is configured differently. Disks can't be changed here: list the "+
				"instance's disks as they are (%s), then add new ones at the end.", i+1, diskList(current[i:i+1]), diskList(current))
		}
	}
	return ""
}

func diskList(vs []dataVolumeModel) string {
	out := make([]string, len(vs))
	for i, v := range vs {
		out[i] = fmt.Sprintf("%s %d GB", v.Name.ValueString(), v.Size.ValueInt64())
	}
	if len(out) == 0 {
		return "none"
	}
	return strings.Join(out, ", ")
}

// ValidateConfig refuses a scale block that can't work, as mtncloud_instance does.
func (r *instanceSettingsResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var sc types.Object
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("scale"), &sc)...)
	checkScale(ctx, sc, &resp.Diagnostics)
}

func (r *instanceSettingsResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	if !r.ready(&resp.Diagnostics) {
		return
	}
	var plan, state instanceSettingsModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	timeout, d := plan.Timeouts.Update(ctx, instanceTimeout)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	id, _ := strconv.ParseInt(state.ID.ValueString(), 10, 64)
	inst, err := r.client.GetInstance(ctx, id)
	if err != nil {
		resp.Diagnostics.AddError("Unable to read instance", err.Error())
		return
	}
	plan.ID = state.ID
	pending, d := req.Private.GetKey(ctx, fipUnconfirmedKey)
	resp.Diagnostics.Append(d...)
	partial := fromPool(ctx, req.Private, state.FloatingIPPool, plan.FloatingIPPool, &resp.Diagnostics)
	unconfirmed := r.apply(ctx, &plan, state, inst, pending != nil, partial, &resp.Diagnostics)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
	// Every server has its address (not just one of them): nothing is still on its way.
	complete := plan.PublicIP.ValueString() != "" && len(plan.PublicIPs.Elements()) >= len(inst.ContainerIDs)
	switch {
	case unconfirmed:
		recordUnconfirmed(ctx, resp.Private, &resp.Diagnostics)
	case complete || plan.FloatingIPPool.IsNull():
		resp.Diagnostics.Append(resp.Private.SetKey(ctx, fipUnconfirmedKey, nil)...)
	}
	if complete && !resp.Diagnostics.HasError() {
		resp.Diagnostics.Append(resp.Private.SetKey(ctx, fipPartialKey, nil)...)
	}
}

// apply sends the settings in plan that differ from was (the VM's values), waits, and reads the
// instance back into plan. A setting left out (unknown in the plan) is never sent. pending says an
// earlier attach was sent but never seen; unconfirmed, that this one was.
//
// restart_trigger and floating_ip_pool are only what the configuration asked for (refresh never
// reads them), so when apply stops before their step, they keep was: the next apply tries again.
func (r *instanceSettingsResource) apply(ctx context.Context, plan *instanceSettingsModel, was instanceSettingsModel, inst *client.Instance, pending bool, partial types.String, diags *diag.Diagnostics) (unconfirmed bool) {
	restarted, attached := false, false
	defer func() {
		if !restarted {
			plan.RestartTrigger = was.RestartTrigger
		}
		if !attached && !unconfirmed {
			plan.FloatingIPPool = was.FloatingIPPool
		}
	}()
	id := inst.ID
	set := func(v interface{ IsUnknown() bool }) bool { return !v.IsUnknown() }
	if (set(plan.Description) && !plan.Description.Equal(was.Description)) || (set(plan.Labels) && !plan.Labels.Equal(was.Labels)) ||
		(set(plan.Tags) && !plan.Tags.Equal(was.Tags)) {
		u := client.InstanceUpdate{Description: inst.Description, Environment: inst.Environment, Labels: inst.Labels, Tags: inst.Tags}
		if set(plan.Description) {
			u.Description = plan.Description.ValueString()
		}
		if set(plan.Labels) {
			u.Labels = stringsOf(ctx, plan.Labels, diags)
		}
		if set(plan.Tags) {
			u.Tags = mapOf(ctx, plan.Tags, diags)
		}
		if err := r.client.UpdateInstance(ctx, id, u); err != nil {
			diags.AddError("Unable to update instance", err.Error())
			r.settle(ctx, plan, inst, nil, diags)
			return
		}
	}
	p, err := r.client.PlacementForGroup(ctx, inst.GroupName)
	if err != nil {
		diags.AddError("Unable to read the instance's group", err.Error())
		r.settle(ctx, plan, inst, nil, diags)
		return
	}
	if set(plan.SecurityGroups) && !plan.SecurityGroups.Equal(was.SecurityGroups) {
		ids, err := r.client.SecurityGroupIDs(ctx, p, inst.ProjectID, stringsOf(ctx, plan.SecurityGroups, diags))
		if err != nil {
			diags.AddAttributeError(path.Root("security_groups"), "Unknown security group", err.Error())
			r.settle(ctx, plan, inst, nil, diags)
			return
		}
		if err := r.client.SetInstanceSecurityGroups(ctx, id, ids); err != nil {
			diags.AddError("Unable to set the instance's security groups", err.Error())
			r.settle(ctx, plan, inst, nil, diags)
			return
		}
	}
	if !plan.Scale.IsUnknown() && !plan.Scale.IsNull() && !plan.Scale.Equal(was.Scale) {
		if err := r.client.SetInstanceThreshold(ctx, id, thresholdOf(ctx, plan.Scale, diags)); err != nil {
			diags.AddAttributeError(path.Root("scale"), "Unable to set the instance's scaling", err.Error())
			r.settle(ctx, plan, inst, nil, diags)
			return
		}
	}
	var werr error
	// New disks: those configured past the ones the instance has (checked to match at plan, and
	// here for an adopt, which has no state to check against).
	var newDisks []dataVolumeModel
	if pv := volumesOf(ctx, plan.DataVolumes, diags); len(pv) > 0 {
		cur := dataDisks(inst)
		if msg := disksAppendOnly(pv, cur); msg != "" {
			diags.AddAttributeError(path.Root("data_volume"), "Disk can't change in place", msg)
			r.settle(ctx, plan, inst, nil, diags)
			return
		}
		newDisks = pv[len(cur):]
	}
	resizePlan := set(plan.Plan) && !strings.EqualFold(plan.Plan.ValueString(), inst.PlanName)
	if resizePlan || len(newDisks) > 0 {
		planID := inst.PlanID
		if resizePlan {
			image, err := r.client.ImageByName(ctx, inst.ImageName)
			if err != nil {
				diags.AddError("Unable to read the instance's image", err.Error())
				r.settle(ctx, plan, inst, nil, diags)
				return
			}
			pl, err := r.client.PlanByName(ctx, p, inst.ProjectID, image.LayoutID, plan.Plan.ValueString())
			if err != nil {
				diags.AddAttributeError(path.Root("plan"), "Unknown plan", err.Error())
				r.settle(ctx, plan, inst, nil, diags)
				return
			}
			planID = pl.ID
		}
		// Kept volumes go with their IDs; new disks, at the end, without.
		resize := client.ResizeInput{PlanID: planID}
		for _, v := range inst.Volumes {
			resize.Volumes = append(resize.Volumes, client.VolumeSpec{ID: v.ID, Root: v.Root, Name: v.Name, Size: v.Size, DatastoreID: v.DatastoreID})
		}
		for _, d := range newDisks {
			spec := client.VolumeSpec{Size: d.Size.ValueInt64()}
			if known(d.Name) {
				spec.Name = d.Name.ValueString()
			}
			ds, ok := (&instanceResource{r.base}).datastoreID(ctx, p, inst.ProjectID, d.Datastore, diags)
			if !ok {
				r.settle(ctx, plan, inst, nil, diags)
				return
			}
			spec.DatastoreID = ds
			resize.Volumes = append(resize.Volumes, spec)
		}
		if err := r.client.ResizeInstance(ctx, id, resize); err != nil {
			diags.AddError("Unable to reconfigure instance", err.Error())
			r.settle(ctx, plan, inst, nil, diags)
			return
		}
		got, err := r.client.WaitForResize(ctx, id, planID, len(resize.Volumes), 0)
		if errors.Is(err, context.DeadlineExceeded) && got != nil {
			diags.AddAttributeError(path.Root("plan"), "Resize not applied",
				fmt.Sprintf("MTN Cloud accepted the change to instance %d, but by the timeout it was on plan %q (asked for %q) "+
					"with %d of %d disks, status %q. Check it in the console, then apply again, or raise `timeouts { update = \"60m\" }`.",
					id, got.PlanName, plan.Plan.ValueString(), len(got.Volumes), len(resize.Volumes), got.Status))
			err = nil
		}
		if got != nil {
			inst = got
		}
		werr = err
	}
	if werr == nil && !client.InstanceFailed(inst.Status) {
		// The instance resource's power handling, on this resource's power_state.
		m := instanceModel{PowerState: plan.PowerState}
		// The VM is someone else's: a trigger set for the first time (create, import) isn't a change.
		restart := !plan.RestartTrigger.IsNull() && !was.RestartTrigger.IsNull() && !plan.RestartTrigger.Equal(was.RestartTrigger)
		got, err := (&instanceResource{r.base}).power(ctx, &m, inst, restart, false, diags)
		if got != nil {
			inst = got
		}
		werr = err
		restarted = err == nil && !diags.HasError()
		if m.PowerState.IsNull() {
			plan.PowerState = types.StringUnknown() // refused: read back from the VM
		}
	}
	switch {
	case werr != nil || diags.HasError():
	case !known(plan.FloatingIPPool) || (sameName(plan.FloatingIPPool, was.FloatingIPPool) && (!inst.MissingPublicIP() || pending)):
		// Unchanged, and the address is there, or an earlier attach may yet bring it: nothing sent.
		attached = true
	default:
		from := was.FloatingIPPool
		if inst.NoPublicIP() || inst.MissingPublicIP() && sameName(plan.FloatingIPPool, was.FloatingIPPool) {
			from = types.StringNull() // nothing to release, or only servers that lost theirs to attach
		}
		if from.IsNull() && known(partial) {
			from = partial // the servers that kept an address are on another pool: they move too
		}
		got, done, err := (&instanceResource{r.base}).setFloatingIP(ctx, inst, from, plan.FloatingIPPool)
		if got != nil {
			inst = got
		}
		switch {
		case err == nil:
			attached = true
		case done.unconfirmed:
			// Sent but not seen: recorded as asked, so the next apply doesn't attach a second one.
			diags.AddAttributeError(path.Root("floating_ip_pool"), "Floating IP not confirmed", err.Error())
			unconfirmed = true
		default:
			diags.AddAttributeError(path.Root("floating_ip_pool"), "Unable to attach the floating IP", err.Error())
			if done.released {
				plan.FloatingIPPool, attached = types.StringNull(), true // the old one is gone: none
			}
		}
	}
	r.settle(ctx, plan, inst, werr, diags)
	return unconfirmed
}

// settle reports a wait's error and reads the instance back into m.
func (r *instanceSettingsResource) settle(ctx context.Context, m *instanceSettingsModel, inst *client.Instance, err error, diags *diag.Diagnostics) {
	switch {
	case err != nil && errors.Is(err, context.DeadlineExceeded):
		diags.AddError("Timed out waiting for the instance",
			fmt.Sprintf("Instance %d is still %q after the timeout. Check it in the console, or raise `timeouts { update = \"60m\" }`.", inst.ID, inst.Status))
	case err != nil:
		diags.AddError("Unable to read instance", err.Error())
	}
	if got, gerr := r.client.GetInstance(context.WithoutCancel(ctx), inst.ID); gerr == nil {
		inst = got
	}
	if client.InstanceFailed(inst.Status) && !diags.HasError() {
		msg := inst.StatusMessage
		if msg == "" {
			msg = "no reason given"
		}
		diags.AddError("Instance change failed",
			fmt.Sprintf("MTN Cloud reports instance %d as %q: %s. Check it in the console, then apply again.", inst.ID, inst.Status, msg))
	}
	r.refresh(ctx, m, inst, diags)
}

// refresh copies the VM's settings into m. A configured value spelled differently (a plan's case)
// is kept, so there is no diff.
func (r *instanceSettingsResource) refresh(ctx context.Context, m *instanceSettingsModel, in *client.Instance, diags *diag.Diagnostics) {
	m.ID = types.StringValue(strconv.FormatInt(in.ID, 10))
	m.InstanceID = m.ID
	m.Name = types.StringValue(in.Name)
	m.Group = types.StringValue(in.GroupName)
	m.Image = types.StringValue(in.ImageName)
	m.Description = types.StringValue(in.Description)
	if !strings.EqualFold(m.Plan.ValueString(), in.PlanName) || m.Plan.IsUnknown() {
		m.Plan = types.StringValue(in.PlanName)
	}
	labels, _ := types.SetValueFrom(ctx, types.StringType, append([]string{}, in.Labels...))
	m.Labels = labels
	tags := in.Tags
	if tags == nil {
		tags = map[string]string{}
	}
	m.Tags, _ = types.MapValueFrom(ctx, types.StringType, tags)
	m.Status = types.StringValue(in.Status)
	if p := client.PowerOf(in.Status); p != "" {
		m.PowerState = types.StringValue(p)
	} else if m.PowerState.IsUnknown() {
		m.PowerState = types.StringNull()
	}
	m.PublicIPs = serverIPs(ctx, r.client, in, diags) // may set in.PublicIP from a server
	m.PrivateIP, m.PublicIP = optional(in.PrivateIP), optional(in.PublicIP)
	m.ExpireDate = optional(in.ExpireDate)
	if m.DataVolumes.IsUnknown() || len(m.DataVolumes.Elements()) > 0 { // managed: show the disks as they are
		prior := volumesOf(ctx, m.DataVolumes, diags)
		vols := dataDisks(in)
		if p, err := r.client.PlacementForGroup(ctx, in.GroupName); err == nil {
			for i := range vols {
				var was types.String
				if i < len(prior) {
					was = prior[i].Datastore
				}
				vols[i].Datastore = (&instanceResource{r.base}).datastoreName(ctx, p, in.ProjectID, in.Volumes[dataIndex(in, i)].DatastoreID, was)
			}
		}
		m.DataVolumes, _ = types.ListValueFrom(ctx, dataVolumeType, vols)
	} else if m.DataVolumes.IsNull() {
		m.DataVolumes = types.ListValueMust(dataVolumeType, nil)
	}
	if !m.Scale.IsNull() { // managed: read it, so a change in the console shows as drift
		th, err := r.client.InstanceThreshold(ctx, in.ID)
		if err != nil {
			diags.AddError("Unable to read the instance's scaling", err.Error())
			return
		}
		m.Scale = scaleOf(ctx, th)
	}
	groups, err := r.client.InstanceSecurityGroups(ctx, in.ID)
	if err != nil {
		diags.AddError("Unable to read the instance's security groups", err.Error())
		return
	}
	m.SecurityGroups = securityGroupNames(ctx, groups, m.SecurityGroups)
}

// Delete leaves the VM: whatever owns it deletes it.
func (r *instanceSettingsResource) Delete(context.Context, resource.DeleteRequest, *resource.DeleteResponse) {
}

func (r *instanceSettingsResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	if !digits.MatchString(req.ID) {
		resp.Diagnostics.AddError("Invalid import ID", fmt.Sprintf("Import by the instance's numeric ID, for example 9701. Got %q.", req.ID))
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("instance_id"), req.ID)...)
}

// dataDisks are the instance's disks after the root one, without datastores.
func dataDisks(in *client.Instance) []dataVolumeModel {
	var out []dataVolumeModel
	for _, v := range in.Volumes {
		if !v.Root {
			out = append(out, dataVolumeModel{Name: types.StringValue(v.Name), Size: types.Int64Value(v.Size), Datastore: types.StringNull()})
		}
	}
	return out
}

// dataIndex is the index in in.Volumes of the i-th data disk.
func dataIndex(in *client.Instance, i int) int {
	n := -1
	for j, v := range in.Volumes {
		if !v.Root {
			if n++; n == i {
				return j
			}
		}
	}
	return -1
}
