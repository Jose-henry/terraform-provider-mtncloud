package compute

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/setplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/mtn/terraform-provider-mtncloud/internal/client"
)

// appSettingsResource manages the settings of an app something else owns: a managed service's or
// self-managed Kubernetes order's app, or one made in the console (B-028). It never creates or
// deletes the app.
type appSettingsResource struct{ base }

func newAppSettings() resource.Resource { return &appSettingsResource{} }

var (
	_ resource.ResourceWithConfigure   = (*appSettingsResource)(nil)
	_ resource.ResourceWithImportState = (*appSettingsResource)(nil)
	_ resource.ResourceWithModifyPlan  = (*appSettingsResource)(nil)
)

type appSettingsModel struct {
	ID          types.String `tfsdk:"id"`
	AppID       types.String `tfsdk:"app_id"`
	Name        types.String `tfsdk:"name"`
	Description types.String `tfsdk:"description"`
	Labels      types.Set    `tfsdk:"labels"`
	Environment types.String `tfsdk:"environment"`
	Owner       types.String `tfsdk:"owner"`
	Group       types.String `tfsdk:"group"`
	Status      types.String `tfsdk:"status"`
	InstanceIDs types.Set    `tfsdk:"instance_ids"`
}

func (r *appSettingsResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_app_settings"
}

func (r *appSettingsResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	keep := []planmodifier.String{stringplanmodifier.UseStateForUnknown()}
	// Every setting is Optional+Computed: left out, it isn't managed and shows the app's own value.
	resp.Schema = schema.Schema{
		Description: "Settings of an app this configuration doesn't own: a managed service's or self-managed Kubernetes " +
			"order's app, or one made in the console. Create adopts the app by `app_id` and changes only the settings " +
			"you set; settings you leave out aren't managed and show the app's own values. **Destroy leaves the app**: it " +
			"only stops managing these settings. An order keeps its app by ID (`app_id` on the order), so renaming it " +
			"here doesn't stop the order's destroy from finding it. Add instances to an app with " +
			"`mtncloud_app_instance`. VMs are resized, powered and given disks through their instances " +
			"(`mtncloud_instance_settings`): an app has no such settings of its own.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{Computed: true, PlanModifiers: keep, Description: "The app's ID."},
			"app_id": schema.StringAttribute{Required: true,
				Description:   "ID of the app, for example a managed service order's `app_id`. Changing it adopts another app.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
				Validators:    []validator.String{stringvalidator.RegexMatches(digits, "must be an app ID")}},
			"name": schema.StringAttribute{Optional: true, Computed: true,
				Description: "The app's name, changed in place. The order that made it keeps finding it by ID."},
			"description": schema.StringAttribute{Optional: true, Computed: true, Description: "Description, changed in place."},
			"labels": schema.SetAttribute{Optional: true, Computed: true, ElementType: types.StringType,
				Description:   "Labels. Set, they replace the app's labels in place; `[]` clears them.",
				PlanModifiers: []planmodifier.Set{setplanmodifier.UseStateForUnknown()}},
			"environment": schema.StringAttribute{Optional: true, Computed: true,
				Description: "Environment by name or code, for example `Dev`. Changed in place."},
			"owner": schema.StringAttribute{Optional: true, Computed: true,
				Description: "Owner's username in your tenant. **Changing it also changes the owner of every instance in the " +
					"app**, which changes who can see and manage them."},
			"group":        schema.StringAttribute{Computed: true, PlanModifiers: keep, Description: "The app's group, read only."},
			"status":       schema.StringAttribute{Computed: true, Description: "The app's status, for example `running`."},
			"instance_ids": schema.SetAttribute{Computed: true, ElementType: types.StringType, Description: "IDs of the instances in the app."},
		},
	}
}

// ModifyPlan warns before an owner change, which reaches every instance in the app.
func (r *appSettingsResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.Plan.Raw.IsNull() || req.State.Raw.IsNull() {
		return
	}
	var plan, state appSettingsModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if known(plan.Owner) && !strings.EqualFold(plan.Owner.ValueString(), state.Owner.ValueString()) {
		resp.Diagnostics.AddAttributeWarning(path.Root("owner"), "Owner changes for every instance",
			fmt.Sprintf("Changing the owner of app %s to %q also makes that user the owner of every instance in it.",
				state.ID.ValueString(), plan.Owner.ValueString()))
	}
}

func (r *appSettingsResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	if !r.ready(&resp.Diagnostics) {
		return
	}
	var plan appSettingsModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	id, _ := strconv.ParseInt(plan.AppID.ValueString(), 10, 64)
	app, err := r.client.GetApp(ctx, id)
	if err != nil {
		resp.Diagnostics.AddAttributeError(path.Root("app_id"), "Unable to read app "+plan.AppID.ValueString(),
			err.Error()+". Check the ID: the app must exist and be visible to this login.")
		return
	}
	var current appSettingsModel
	r.refresh(ctx, &current, app, &resp.Diagnostics)
	r.apply(ctx, &plan, current, app, &resp.Diagnostics)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *appSettingsResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	if !r.ready(&resp.Diagnostics) {
		return
	}
	var state appSettingsModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	id, err := strconv.ParseInt(state.ID.ValueString(), 10, 64)
	if err != nil {
		resp.Diagnostics.AddError("Invalid app ID", fmt.Sprintf("%q is not an app ID: %s", state.ID.ValueString(), err))
		return
	}
	app, err := r.client.GetApp(ctx, id)
	if client.IsNotFound(err) {
		resp.State.RemoveResource(ctx) // deleted with whatever owned it (rule 8)
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Unable to read app", err.Error())
		return
	}
	r.refresh(ctx, &state, app, &resp.Diagnostics)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *appSettingsResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	if !r.ready(&resp.Diagnostics) {
		return
	}
	var plan, state appSettingsModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	id, _ := strconv.ParseInt(state.ID.ValueString(), 10, 64)
	app, err := r.client.GetApp(ctx, id)
	if err != nil {
		resp.Diagnostics.AddError("Unable to read app", err.Error())
		return
	}
	r.apply(ctx, &plan, state, app, &resp.Diagnostics)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// apply sends the settings in plan that differ from was, then reads the app back into plan. A
// setting left out (unknown in the plan) is never sent.
func (r *appSettingsResource) apply(ctx context.Context, plan *appSettingsModel, was appSettingsModel, app *client.AppDetail, diags *diag.Diagnostics) {
	var u client.AppUpdate
	str := func(p, w types.String) *string {
		if !known(p) || p.Equal(w) {
			return nil
		}
		v := p.ValueString()
		return &v
	}
	u.Name, u.Description = str(plan.Name, was.Name), str(plan.Description, was.Description)
	if !plan.Labels.IsUnknown() && !plan.Labels.Equal(was.Labels) {
		u.SetLabels, u.Labels = true, stringsOf(ctx, plan.Labels, diags)
		slices.Sort(u.Labels)
	}
	if known(plan.Environment) && !sameName(plan.Environment, was.Environment) {
		code := "" // "" clears it, as mtncloud_app does
		if plan.Environment.ValueString() != "" {
			env, err := r.client.EnvironmentByName(ctx, plan.Environment.ValueString())
			if err != nil {
				diags.AddAttributeError(path.Root("environment"), "Unknown environment", err.Error())
				r.refresh(ctx, plan, app, diags)
				return
			}
			code = env.Value
		}
		u.Environment = &code
	}
	if known(plan.Owner) && !strings.EqualFold(plan.Owner.ValueString(), app.OwnerName) {
		owner, err := r.client.UserIDByName(ctx, plan.Owner.ValueString())
		if err != nil {
			diags.AddAttributeError(path.Root("owner"), "Unknown user", err.Error())
			r.refresh(ctx, plan, app, diags)
			return
		}
		u.OwnerID = owner
	}
	if err := r.client.UpdateApp(ctx, app.ID, u); err != nil {
		diags.AddError("Unable to update app "+strconv.FormatInt(app.ID, 10), err.Error())
	}
	if got, err := r.client.GetApp(ctx, app.ID); err == nil {
		app = got
	}
	r.refresh(ctx, plan, app, diags)
}

// refresh copies the app into m, keeping a configured spelling (an environment's name, a
// username's case) of the same value.
func (r *appSettingsResource) refresh(ctx context.Context, m *appSettingsModel, a *client.AppDetail, diags *diag.Diagnostics) {
	m.ID = types.StringValue(strconv.FormatInt(a.ID, 10))
	m.AppID = m.ID
	m.Name = types.StringValue(a.Name)
	m.Description = types.StringValue(a.Description)
	labels := append([]string{}, a.Labels...)
	set, d := types.SetValueFrom(ctx, types.StringType, labels)
	diags.Append(d...)
	m.Labels = set
	m.Environment = (&instanceResource{r.base}).environmentName(ctx, a.Environment, m.Environment)
	if m.Environment.IsNull() {
		m.Environment = types.StringValue("")
	}
	if !strings.EqualFold(m.Owner.ValueString(), a.OwnerName) || !known(m.Owner) {
		m.Owner = types.StringValue(a.OwnerName)
	}
	m.Group = types.StringValue(a.GroupName)
	m.Status = types.StringValue(a.Status)
	ids := make([]string, 0, len(a.Tiers))
	for id := range a.Tiers {
		ids = append(ids, strconv.FormatInt(id, 10))
	}
	m.InstanceIDs, d = types.SetValueFrom(ctx, types.StringType, ids)
	diags.Append(d...)
}

// Delete leaves the app: whatever owns it deletes it.
func (r *appSettingsResource) Delete(context.Context, resource.DeleteRequest, *resource.DeleteResponse) {
}

func (r *appSettingsResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	if !digits.MatchString(req.ID) {
		resp.Diagnostics.AddError("Invalid import ID", fmt.Sprintf("Import by the app's numeric ID, for example 9901. Got %q.", req.ID))
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("app_id"), req.ID)...)
}
