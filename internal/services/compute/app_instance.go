package compute

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/mtn/terraform-provider-mtncloud/internal/client"
)

// appInstanceResource puts one existing instance in an app's tier (B-028 membership). It owns the
// membership only: the instance and the app belong to whatever made them.
type appInstanceResource struct{ base }

func newAppInstance() resource.Resource { return &appInstanceResource{} }

var _ resource.ResourceWithImportState = (*appInstanceResource)(nil)

type appInstanceModel struct {
	ID         types.String `tfsdk:"id"`
	AppID      types.String `tfsdk:"app_id"`
	InstanceID types.String `tfsdk:"instance_id"`
	Tier       types.String `tfsdk:"tier"`
}

func (r *appInstanceResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_app_instance"
}

func (r *appInstanceResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	replace := []planmodifier.String{stringplanmodifier.RequiresReplace()}
	id := []validator.String{stringvalidator.RegexMatches(digits, "must be an ID")}
	resp.Schema = schema.Schema{
		Description: "An existing instance in an app's tier. Create adds it to the app; **destroy takes it out of the app " +
			"and leaves it running** (delete the instance through whatever owns it). Taking a node out of a managed " +
			"service's app can break the service. Destroying the order that made the app takes instances added this way " +
			"out of it first and leaves them running.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{Computed: true, PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
				Description: "`<app_id>/<instance_id>`."},
			"app_id": schema.StringAttribute{Required: true, PlanModifiers: replace, Validators: id,
				Description: "ID of the app, for example an order's `app_id`."},
			"instance_id": schema.StringAttribute{Required: true, PlanModifiers: replace, Validators: id,
				Description: "ID of the instance, for example `mtncloud_instance.worker.id`."},
			"tier": schema.StringAttribute{Required: true, PlanModifiers: replace,
				Description: "Tier to put it in, for example `App` or `Worker`. A tier the app doesn't have yet is created. " +
					"Changing it moves the instance (out, then in)."},
		},
	}
}

// appInstanceID is the resource's ID, "<app>/<instance>", and parseAppInstanceID reads it.
func appInstanceID(app, inst int64) string { return fmt.Sprintf("%d/%d", app, inst) }

func parseAppInstanceID(id string) (app, inst int64, err error) {
	a, i, ok := strings.Cut(id, "/")
	app, err1 := strconv.ParseInt(a, 10, 64)
	inst, err2 := strconv.ParseInt(i, 10, 64)
	if !ok || err1 != nil || err2 != nil || app <= 0 || inst <= 0 {
		return 0, 0, fmt.Errorf("%q isn't <app_id>/<instance_id>, for example 9901/9701", id)
	}
	return app, inst, nil
}

func (r *appInstanceResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	if !r.ready(&resp.Diagnostics) {
		return
	}
	var m appInstanceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	app, _ := strconv.ParseInt(m.AppID.ValueString(), 10, 64)
	inst, _ := strconv.ParseInt(m.InstanceID.ValueString(), 10, 64)
	// Already there (a retry, or added in the console): adopt it rather than add it twice (rule 11).
	a, err := r.client.GetApp(ctx, app)
	if err != nil {
		resp.Diagnostics.AddAttributeError(path.Root("app_id"), "Unable to read app "+m.AppID.ValueString(), err.Error())
		return
	}
	if tier, in := a.Tiers[inst]; in && !strings.EqualFold(tier, m.Tier.ValueString()) {
		resp.Diagnostics.AddAttributeError(path.Root("tier"), "Instance already in the app",
			fmt.Sprintf("Instance %d is already in app %d, in tier %q. Set tier = %q, or take it out of the app first.", inst, app, tier, tier))
		return
	} else if !in {
		if err := r.client.AddAppInstance(ctx, app, inst, m.Tier.ValueString()); err != nil {
			resp.Diagnostics.AddError(fmt.Sprintf("Unable to add instance %d to app %d", inst, app), err.Error())
			return
		}
	}
	m.ID = types.StringValue(appInstanceID(app, inst))
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

func (r *appInstanceResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	if !r.ready(&resp.Diagnostics) {
		return
	}
	var m appInstanceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &m)...)
	app, inst, err := parseAppInstanceID(m.ID.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Invalid ID", err.Error())
		return
	}
	a, err := r.client.GetApp(ctx, app)
	if client.IsNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Unable to read app", err.Error())
		return
	}
	tier, in := a.Tiers[inst]
	if !in {
		resp.State.RemoveResource(ctx) // taken out of the app outside Terraform: plan adding it again
		return
	}
	m.AppID, m.InstanceID = types.StringValue(strconv.FormatInt(app, 10)), types.StringValue(strconv.FormatInt(inst, 10))
	if !strings.EqualFold(tier, m.Tier.ValueString()) {
		m.Tier = types.StringValue(tier) // moved in the console: plan moving it back
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

// Update never runs: every argument replaces.
func (r *appInstanceResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	resp.Diagnostics.AddError("App membership can't change in place", "This is a provider bug: every argument should replace it.")
}

func (r *appInstanceResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	if !r.ready(&resp.Diagnostics) {
		return
	}
	var m appInstanceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &m)...)
	app, inst, err := parseAppInstanceID(m.ID.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Invalid ID", err.Error())
		return
	}
	if err := r.client.RemoveAppInstance(ctx, app, inst); err != nil {
		resp.Diagnostics.AddError(fmt.Sprintf("Unable to take instance %d out of app %d", inst, app), err.Error())
	}
}

func (r *appInstanceResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	if _, _, err := parseAppInstanceID(req.ID); err != nil {
		resp.Diagnostics.AddError("Invalid import ID", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
}
