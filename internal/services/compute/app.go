package compute

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/setplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/mtn/terraform-provider-mtncloud/internal/client"
)

// appResource is a blank app this configuration owns (B-017): a named set of instances that join it
// through mtncloud_app_instance. Destroy deletes the app and leaves the instances.
type appResource struct{ base }

func newApp() resource.Resource { return &appResource{} }

var (
	_ resource.ResourceWithImportState = (*appResource)(nil)
	_ resource.ResourceWithModifyPlan  = (*appResource)(nil)
)

type appModel struct {
	ID          types.String `tfsdk:"id"`
	Name        types.String `tfsdk:"name"`
	Description types.String `tfsdk:"description"`
	Labels      types.Set    `tfsdk:"labels"`
	Environment types.String `tfsdk:"environment"`
	Group       types.String `tfsdk:"group"`
	Status      types.String `tfsdk:"status"`
	InstanceIDs types.Set    `tfsdk:"instance_ids"`
}

func (r *appResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_app"
}

func (r *appResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	keep := []planmodifier.String{stringplanmodifier.UseStateForUnknown()}
	resp.Schema = schema.Schema{
		Description: "A blank app: a named set of existing instances, shown together in the console. Add instances with " +
			"`mtncloud_app_instance`. **Destroy deletes the app and leaves its instances running.** For an app a catalog " +
			"order made, use `mtncloud_app_settings`.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{Computed: true, PlanModifiers: keep, Description: "The app's ID."},
			"name": schema.StringAttribute{Required: true,
				Description: "Name, unique in your tenant. Changed in place."},
			// Optional+Computed: left out, the app keeps what it has (none on create).
			"description": schema.StringAttribute{Optional: true, Computed: true, PlanModifiers: keep,
				Description: "Description, changed in place."},
			"labels": schema.SetAttribute{Optional: true, Computed: true, ElementType: types.StringType,
				PlanModifiers: []planmodifier.Set{setplanmodifier.UseStateForUnknown()},
				Description:   "Labels, changed in place; `[]` clears them."},
			"environment": schema.StringAttribute{Optional: true, Computed: true, PlanModifiers: keep,
				Description: "Environment by name or code, for example `Production`. Changed in place."},
			// Optional+Computed: left out, the provider's group is used, and state keeps it.
			"group": schema.StringAttribute{Optional: true, Computed: true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown(), stringplanmodifier.RequiresReplace()},
				Description:   "Group, by name, for example `MTNNG_CLOUD_AZ_1`. Defaults to the provider's `group`. Changing it replaces the app."},
			"status":       schema.StringAttribute{Computed: true, Description: "The app's status, for example `running`."},
			"instance_ids": schema.SetAttribute{Computed: true, ElementType: types.StringType, Description: "IDs of the instances in the app."},
		},
	}
}

// ModifyPlan fills the provider's group when the configuration leaves it out.
func (r *appResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.Plan.Raw.IsNull() || r.client == nil {
		return
	}
	r.fillDefaults(ctx, &req, resp, "group")
	if req.State.Raw.IsNull() {
		return
	}
	// The provider's group fills a group left out after the attribute's RequiresReplace has run, so a
	// filled group that isn't the app's is checked here: it can't change in place.
	var planned, current types.String
	resp.Diagnostics.Append(resp.Plan.GetAttribute(ctx, path.Root("group"), &planned)...)
	resp.Diagnostics.Append(req.State.GetAttribute(ctx, path.Root("group"), &current)...)
	if known(planned) && !strings.EqualFold(planned.ValueString(), current.ValueString()) {
		resp.RequiresReplace.Append(path.Root("group"))
	} else if known(planned) {
		resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("group"), current)...)
	}
}

func (r *appResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	if !r.ready(&resp.Diagnostics) {
		return
	}
	var plan appModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	// Names are unique in a tenant. One already there (a retry after a lost answer, or made in the
	// console) is never taken silently (rule 11): it may hold someone else's instances.
	apps, err := r.client.ListApps(ctx)
	if err != nil {
		resp.Diagnostics.AddError("Unable to list apps", err.Error())
		return
	}
	if same := client.AppsNamed(apps, plan.Name.ValueString()); len(same) > 0 {
		resp.Diagnostics.AddAttributeError(path.Root("name"), "App name in use",
			fmt.Sprintf("An app named %q already exists (ID %d). Pick another name, or import it: terraform import <address> %d.",
				plan.Name.ValueString(), same[0].ID, same[0].ID))
		return
	}
	p, err := r.client.PlacementForGroup(ctx, plan.Group.ValueString())
	if err != nil {
		resp.Diagnostics.AddAttributeError(path.Root("group"), "Unknown group", err.Error())
		return
	}
	in := client.AppCreate{Name: plan.Name.ValueString(), Description: plan.Description.ValueString(), GroupID: p.GroupID, CloudID: p.CloudID}
	if known(plan.Environment) && plan.Environment.ValueString() != "" {
		env, err := r.client.EnvironmentByName(ctx, plan.Environment.ValueString())
		if err != nil {
			resp.Diagnostics.AddAttributeError(path.Root("environment"), "Unknown environment", err.Error())
			return
		}
		in.Environment = env.Value
	}
	if !plan.Labels.IsUnknown() {
		in.Labels = stringsOf(ctx, plan.Labels, &resp.Diagnostics)
		slices.Sort(in.Labels)
	}
	id, err := r.client.CreateApp(ctx, in)
	if err != nil {
		resp.Diagnostics.AddError("Unable to create app "+plan.Name.ValueString(), err.Error())
		return
	}
	a, err := r.client.GetApp(ctx, id)
	if err != nil {
		// Made, but not read back: keep its ID so the next apply reads it instead of making another.
		plan.ID = types.StringValue(strconv.FormatInt(id, 10))
		plan.Status, plan.InstanceIDs = types.StringValue(""), types.SetValueMust(types.StringType, nil)
		if !known(plan.Description) {
			plan.Description = types.StringValue(in.Description)
		}
		if !known(plan.Environment) {
			plan.Environment = types.StringValue("")
		}
		if plan.Labels.IsUnknown() {
			plan.Labels = types.SetValueMust(types.StringType, nil)
		}
		resp.Diagnostics.AddWarning("App made, not read back", err.Error())
		resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
		return
	}
	r.refresh(ctx, &plan, a, &resp.Diagnostics)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *appResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	if !r.ready(&resp.Diagnostics) {
		return
	}
	var state appModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	id, err := strconv.ParseInt(state.ID.ValueString(), 10, 64)
	if err != nil {
		resp.Diagnostics.AddError("Invalid app ID", fmt.Sprintf("%q is not an app ID: %s", state.ID.ValueString(), err))
		return
	}
	a, err := r.client.GetApp(ctx, id)
	if client.IsNotFound(err) {
		resp.State.RemoveResource(ctx) // deleted outside Terraform: plan making it again (rule 8)
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Unable to read app", err.Error())
		return
	}
	r.refresh(ctx, &state, a, &resp.Diagnostics)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *appResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	if !r.ready(&resp.Diagnostics) {
		return
	}
	var plan, state appModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	id, _ := strconv.ParseInt(state.ID.ValueString(), 10, 64)
	var u client.AppUpdate
	str := func(p, w types.String) *string {
		if !known(p) || p.Equal(w) {
			return nil
		}
		v := p.ValueString()
		return &v
	}
	u.Name, u.Description = str(plan.Name, state.Name), str(plan.Description, state.Description)
	if !plan.Labels.IsUnknown() && !plan.Labels.Equal(state.Labels) {
		u.SetLabels, u.Labels = true, stringsOf(ctx, plan.Labels, &resp.Diagnostics)
		slices.Sort(u.Labels)
	}
	if known(plan.Environment) && !sameName(plan.Environment, state.Environment) {
		code := ""
		if plan.Environment.ValueString() != "" {
			env, err := r.client.EnvironmentByName(ctx, plan.Environment.ValueString())
			if err != nil {
				resp.Diagnostics.AddAttributeError(path.Root("environment"), "Unknown environment", err.Error())
				return
			}
			code = env.Value
		}
		u.Environment = &code
	}
	if err := r.client.UpdateApp(ctx, id, u); err != nil {
		resp.Diagnostics.AddError("Unable to update app "+state.ID.ValueString(), err.Error())
		return
	}
	a, err := r.client.GetApp(ctx, id)
	if err != nil {
		resp.Diagnostics.AddError("Unable to read app after the update", err.Error())
		return
	}
	plan.ID = state.ID
	r.refresh(ctx, &plan, a, &resp.Diagnostics)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// refresh copies the app into m, keeping a configured spelling of the same environment or group.
func (r *appResource) refresh(ctx context.Context, m *appModel, a *client.AppDetail, diags *diag.Diagnostics) {
	m.ID = types.StringValue(strconv.FormatInt(a.ID, 10))
	m.Name = types.StringValue(a.Name)
	m.Description = types.StringValue(a.Description)
	labels, d := types.SetValueFrom(ctx, types.StringType, append([]string{}, a.Labels...))
	diags.Append(d...)
	m.Labels = labels
	m.Environment = (&instanceResource{r.base}).environmentName(ctx, a.Environment, m.Environment)
	if m.Environment.IsNull() {
		m.Environment = types.StringValue("")
	}
	if !strings.EqualFold(m.Group.ValueString(), a.GroupName) || !known(m.Group) {
		m.Group = types.StringValue(a.GroupName)
	}
	m.Status = types.StringValue(a.Status)
	ids := make([]string, 0, len(a.Tiers))
	for id := range a.Tiers {
		ids = append(ids, strconv.FormatInt(id, 10))
	}
	m.InstanceIDs, d = types.SetValueFrom(ctx, types.StringType, ids)
	diags.Append(d...)
}

func (r *appResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	if !r.ready(&resp.Diagnostics) {
		return
	}
	var state appModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	id, err := strconv.ParseInt(state.ID.ValueString(), 10, 64)
	if err != nil {
		resp.Diagnostics.AddError("Invalid app ID", err.Error())
		return
	}
	if err := r.client.DeleteAppOnly(ctx, id); err != nil {
		resp.Diagnostics.AddError("Unable to delete app "+state.ID.ValueString(), err.Error())
	}
}

func (r *appResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	if !digits.MatchString(req.ID) {
		resp.Diagnostics.AddError("Invalid import ID", fmt.Sprintf("Import by the app's numeric ID, for example 9901. Got %q.", req.ID))
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
}
