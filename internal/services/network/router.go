package network

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-framework-timeouts/resource/timeouts"
	"github.com/hashicorp/terraform-plugin-framework-validators/setvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/mtn/terraform-provider-mtncloud/internal/client"
	"github.com/mtn/terraform-provider-mtncloud/internal/normalize"
)

// A router is usually "ok" within seconds (captured 2026-10-27); these bound a stuck one.
const routerTimeout = 10 * time.Minute

var (
	_ resource.ResourceWithConfigure   = (*routerResource)(nil)
	_ resource.ResourceWithImportState = (*routerResource)(nil)
	_ resource.ResourceWithModifyPlan  = (*routerResource)(nil)
)

func newRouter() resource.Resource { return &routerResource{} }

type routerResource struct{ base }

type routerModel struct {
	ID              types.String   `tfsdk:"id"`
	Name            types.String   `tfsdk:"name"`
	Description     types.String   `tfsdk:"description"`
	Project         types.String   `tfsdk:"project"`
	Networks        types.Set      `tfsdk:"networks"`
	ExternalNetwork types.String   `tfsdk:"external_network"`
	Enabled         types.Bool     `tfsdk:"enabled"`
	ExternalIP      types.String   `tfsdk:"external_ip"`
	ExternalID      types.String   `tfsdk:"external_id"`
	Timeouts        timeouts.Value `tfsdk:"timeouts"`
}

func (r *routerResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_router"
}

func (r *routerResource) Schema(ctx context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "An MTN Router: connects MTN Networks of one project to each other and, through an external network, " +
			"to the outside. Creating it waits until MTN Cloud reports it ready, usually within seconds.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:      true,
				Description:   "Router ID.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"name": schema.StringAttribute{
				Required:      true,
				Description:   "Name of the router. MTN Cloud doesn't allow renaming a router: a new name replaces it.",
				Validators:    []validator.String{stringvalidator.LengthBetween(1, 255)},
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"description": schema.StringAttribute{
				Optional:    true,
				Description: "Description of the router.",
			},
			// Optional+Computed: left out, the provider's project is used, and state keeps it.
			"project": schema.StringAttribute{
				Optional: true, Computed: true,
				Description: "Project the router is created in, by name (for example `web-prod`; the full name with the tenant " +
					"suffix also works). Defaults to the provider's `project`; changing that default later replaces a router that relies on it. Its networks must be in the same project. " +
					"Changing it to another project replaces the router.",
			},
			"networks": schema.SetAttribute{
				Required:    true,
				ElementType: types.StringType,
				Description: "IDs of the MTN Networks the router connects, for example `[mtncloud_network.app.id]`. Networks " +
					"are added and removed in place. Use the `mtncloud_network` data source for networks made outside Terraform.",
				Validators: []validator.Set{
					setvalidator.SizeAtLeast(1),
					setvalidator.ValueStringsAre(stringvalidator.RegexMatches(digits, "must be a network ID")),
				},
			},
			"external_network": schema.StringAttribute{
				Optional: true,
				Description: "External network that gives the router its gateway and external IP, by name, for example " +
					"`public-network-02`. Changing it moves the gateway in place, and the external IP changes. Leave it out " +
					"for a router that only connects your networks.",
				Validators: []validator.String{stringvalidator.LengthAtLeast(1)},
			},
			"enabled": schema.BoolAttribute{
				Optional:    true,
				Computed:    true,
				Default:     booldefault.StaticBool(true),
				Description: "Whether the router is enabled. Defaults to `true`.",
			},
			"external_ip": schema.StringAttribute{
				Computed:    true,
				Description: "The router's address on the external network. Empty without `external_network`.",
			},
			"external_id": schema.StringAttribute{
				Computed:      true,
				Description:   "Router ID in the cloud (the external ID).",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
		},
		Blocks: map[string]schema.Block{
			"timeouts": timeouts.Block(ctx, timeouts.Opts{Create: true, Update: true, Delete: true}),
		},
	}
}

func (r *routerResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.Plan.Raw.IsNull() || r.client == nil {
		return // destroy, or validation before the provider is configured
	}
	if r.fillDefaults(ctx, &req, resp, "project"); resp.Diagnostics.HasError() {
		return
	}
	r.planProject(ctx, req, resp)
	var plan routerModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if v := plan.ExternalNetwork; !v.IsNull() && !v.IsUnknown() {
		if _, err := r.client.ExternalNetworkByName(ctx, v.ValueString()); err != nil {
			resp.Diagnostics.AddAttributeError(path.Root("external_network"), "Unknown external network", err.Error())
		}
	}
	if req.State.Raw.IsNull() {
		return
	}
	var state routerModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	// The external IP changes only with the gateway; otherwise it is known from state.
	if sameName(plan.ExternalNetwork, state.ExternalNetwork) {
		resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("external_ip"), state.ExternalIP)...)
	} else {
		resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("external_ip"), types.StringUnknown())...)
	}
}

// sameName compares two names the way the platform does (case ignored); null equals null.
func sameName(a, b types.String) bool {
	if a.IsUnknown() || b.IsUnknown() {
		return false
	}
	if a.IsNull() || b.IsNull() {
		return a.IsNull() && b.IsNull()
	}
	return strings.EqualFold(a.ValueString(), b.ValueString())
}

// input resolves the planned router into API IDs: the project, the gateway, and each network's
// subnet. A network in another project is refused here, with names, rather than by the API.
func (r *routerResource) input(ctx context.Context, m routerModel, diags *diag.Diagnostics) (client.RouterInput, bool) {
	in := client.RouterInput{Name: m.Name.ValueString(), Description: m.Description.ValueString(), Enabled: m.Enabled.ValueBool()}
	project, err := r.client.ProjectByName(ctx, m.Project.ValueString())
	if err != nil {
		diags.AddAttributeError(path.Root("project"), client.ProjectErrorTitle(err), err.Error())
		return in, false
	}
	in.ProjectID = project.ID
	if !m.ExternalNetwork.IsNull() {
		ext, err := r.client.ExternalNetworkByName(ctx, m.ExternalNetwork.ValueString())
		if err != nil {
			diags.AddAttributeError(path.Root("external_network"), "Unknown external network", err.Error())
			return in, false
		}
		in.ExternalNetworkID = ext.ID
	}
	var ids []string
	diags.Append(m.Networks.ElementsAs(ctx, &ids, false)...)
	slices.Sort(ids) // a stable order, so the same set always sends the same body
	for _, s := range ids {
		id, _ := strconv.ParseInt(s, 10, 64)
		n, err := r.client.GetNetwork(ctx, id)
		if err != nil {
			if client.IsNotFound(err) {
				err = fmt.Errorf("network %s does not exist", s)
			}
			diags.AddAttributeError(path.Root("networks"), "Unable to read network", err.Error())
			return in, false
		}
		if n.ProjectID != project.ID {
			diags.AddAttributeError(path.Root("networks"), "Network in another project",
				fmt.Sprintf("Network %q (%s) is in project %q, but the router is in %q. A router connects networks of its own project.",
					n.Name, s, n.ProjectName, project.Name))
			return in, false
		}
		if n.SubnetID == 0 {
			diags.AddAttributeError(path.Root("networks"), "Network has no subnet",
				fmt.Sprintf("Network %q (%s) has no subnet yet, so a router can't attach to it. Try again in a minute.", n.Name, s))
			return in, false
		}
		in.SubnetIDs = append(in.SubnetIDs, n.SubnetID)
	}
	return in, !diags.HasError()
}

func (r *routerResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	if !r.ready(&resp.Diagnostics) {
		return
	}
	var plan routerModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	timeout, d := plan.Timeouts.Create(ctx, routerTimeout)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	in, ok := r.input(ctx, plan, &resp.Diagnostics)
	if !ok {
		return
	}

	// A router of this name in the project is never adopted: it may not be ours.
	existing, err := r.findInProject(ctx, in.Name, in.ProjectID)
	if err != nil {
		resp.Diagnostics.AddError("Unable to check for an existing router", err.Error())
		return
	}
	if existing != nil {
		resp.Diagnostics.AddAttributeError(path.Root("name"), "Router already exists",
			fmt.Sprintf("Project %q already has a router named %q (ID %d). Import it with `terraform import <address> %d`, or choose another name.",
				plan.Project.ValueString(), in.Name, existing.ID, existing.ID))
		return
	}
	id, err := r.client.CreateRouter(ctx, in)
	if err != nil {
		// The request may have been applied (rule 11): the reply was lost, or the timeout ended the
		// wait for it. None existed a moment ago, so one now is ours. The lookup gets its own short
		// deadline, because ctx may be the one that just ran out.
		if !client.MayHaveSucceeded(err) && ctx.Err() == nil {
			resp.Diagnostics.AddError("Unable to create router", err.Error())
			return
		}
		lookup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		found, ferr := r.findInProject(lookup, in.Name, in.ProjectID)
		cancel()
		if found == nil {
			if ferr != nil {
				err = fmt.Errorf("%w; looking for the router afterwards also failed: %w", err, ferr)
			}
			resp.Diagnostics.AddError("Unable to create router", err.Error())
			return
		}
		id = found.ID
	}
	// From here the router exists: it goes into state whatever happens, so a failed or stuck one is
	// replaced by the next apply instead of being orphaned.
	plan.ID = types.StringValue(strconv.FormatInt(id, 10))
	rt, err := r.client.WaitForRouter(ctx, id)
	r.settle(ctx, &plan, rt, err, "create", &resp.Diagnostics)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// findInProject returns the router with this name in the project, or nil. The API doesn't say
// which project a router is in, so it is told by its first network. A router with no networks
// can't be placed, so it counts as a clash rather than being ignored.
func (r *routerResource) findInProject(ctx context.Context, name string, projectID int64) (*client.Router, error) {
	routers, err := r.client.FindRouters(ctx, name)
	if err != nil {
		return nil, err
	}
	for _, rt := range routers {
		if len(rt.NetworkIDs) == 0 {
			return rt, nil
		}
		n, err := r.client.GetNetwork(ctx, rt.NetworkIDs[0])
		if client.IsNotFound(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if n.ProjectID == projectID {
			return rt, nil
		}
	}
	return nil, nil
}

// settle records the router after a create or update wait and reports a failure or a timeout.
func (r *routerResource) settle(ctx context.Context, m *routerModel, rt *client.Router, err error, op string, diags *diag.Diagnostics) {
	switch {
	case err != nil && errors.Is(err, context.DeadlineExceeded):
		status := "unknown"
		if rt != nil {
			status = rt.Status
		}
		diags.AddError("Timed out waiting for the router",
			fmt.Sprintf("Router %s is still %q after the %s timeout. Check it in the console, or raise `timeouts { %s = \"20m\" }`.",
				m.ID.ValueString(), status, op, op))
	case err != nil:
		diags.AddError("Unable to read router", err.Error())
	case client.RouterFailed(rt.Status):
		diags.AddError("Router "+op+" failed",
			fmt.Sprintf("MTN Cloud reports router %d as %q. Check that its networks and external network are available; "+
				"the next apply replaces it.", rt.ID, rt.Status))
	}
	if rt != nil && rt.ID != 0 {
		refreshRouter(ctx, m, rt, diags)
	} else {
		r.unknownToNull(m)
	}
}

// unknownToNull clears computed values that were never read, so state can be saved.
func (r *routerResource) unknownToNull(m *routerModel) {
	if m.ExternalIP.IsUnknown() {
		m.ExternalIP = types.StringNull()
	}
	if m.ExternalID.IsUnknown() {
		m.ExternalID = types.StringNull()
	}
}

func (r *routerResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	if !r.ready(&resp.Diagnostics) {
		return
	}
	var state routerModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	id, err := strconv.ParseInt(state.ID.ValueString(), 10, 64)
	if err != nil {
		resp.Diagnostics.AddError("Invalid router ID", fmt.Sprintf("%q is not a router ID: %s", state.ID.ValueString(), err))
		return
	}
	rt, err := r.client.GetRouter(ctx, id)
	if client.IsNotFound(err) {
		resp.State.RemoveResource(ctx) // deleted outside Terraform: plan a new one
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Unable to read router", err.Error())
		return
	}
	refreshRouter(ctx, &state, rt, &resp.Diagnostics)
	// The API doesn't echo the project, so it stays as configured. After an import there is none:
	// take it from the first network, since a router only connects networks of its own project.
	if state.Project.IsNull() && len(rt.NetworkIDs) > 0 {
		n, err := r.client.GetNetwork(ctx, rt.NetworkIDs[0])
		if err != nil {
			resp.Diagnostics.AddError("Unable to read the router's project", err.Error())
			return
		}
		state.Project = types.StringValue(normalize.ProjectShortName(n.ProjectName, r.tenant(ctx, &resp.Diagnostics)))
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *routerResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	if !r.ready(&resp.Diagnostics) {
		return
	}
	var plan, state routerModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	timeout, d := plan.Timeouts.Update(ctx, routerTimeout)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}
	if onlyTimeoutsChanged(plan, state) {
		// Nothing the API holds changed (for example timeouts set after an import): record it only.
		state.Timeouts = plan.Timeouts
		resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
		return
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	in, ok := r.input(ctx, plan, &resp.Diagnostics)
	if !ok {
		return
	}
	in.KeepGateway = sameName(plan.ExternalNetwork, state.ExternalNetwork)
	id, _ := strconv.ParseInt(state.ID.ValueString(), 10, 64)
	if err := r.client.UpdateRouter(ctx, id, in); err != nil {
		resp.Diagnostics.AddError("Unable to update router", err.Error())
		return
	}
	plan.ID = state.ID
	rt, err := r.client.WaitForRouter(ctx, id)
	r.settle(ctx, &plan, rt, err, "update", &resp.Diagnostics)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *routerResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	if !r.ready(&resp.Diagnostics) {
		return
	}
	var state routerModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	timeout, d := state.Timeouts.Delete(ctx, routerTimeout)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	id, _ := strconv.ParseInt(state.ID.ValueString(), 10, 64)
	if err := r.client.DeleteRouter(ctx, id); err != nil {
		resp.Diagnostics.AddError("Unable to delete router", err.Error())
		return
	}
	// Wait for it to go, so the networks it used can be deleted straight after.
	if err := r.client.WaitForRouterGone(ctx, id); err != nil {
		resp.Diagnostics.AddError("Router not deleted yet",
			fmt.Sprintf("Router %d still exists after the delete (%s). Check it in the console, or raise `timeouts { delete = \"20m\" }`.", id, err))
	}
}

func (r *routerResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	if _, err := strconv.ParseInt(req.ID, 10, 64); err != nil {
		resp.Diagnostics.AddError("Invalid import ID", fmt.Sprintf("Import a router by its numeric ID, for example 9601. Got %q.", req.ID))
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
}

// refreshRouter copies the API's view of a router into the model, keeping the configuration's
// spelling where it means the same thing (external network case, unset description).
func refreshRouter(ctx context.Context, m *routerModel, rt *client.Router, diags *diag.Diagnostics) {
	m.ID = types.StringValue(strconv.FormatInt(rt.ID, 10))
	m.Name = types.StringValue(rt.Name)
	if rt.Description != "" || !m.Description.IsNull() {
		m.Description = types.StringValue(rt.Description)
	}
	m.Enabled = types.BoolValue(rt.Enabled)
	m.ExternalID = optional(rt.ExternalID)
	m.ExternalIP = optional(rt.ExternalIP)
	switch {
	case rt.ExternalNetworkID == 0:
		m.ExternalNetwork = types.StringNull()
	case m.ExternalNetwork.IsNull() || m.ExternalNetwork.IsUnknown() || !strings.EqualFold(m.ExternalNetwork.ValueString(), rt.ExternalNetworkName):
		m.ExternalNetwork = types.StringValue(rt.ExternalNetworkName)
	}
	ids := make([]string, len(rt.NetworkIDs))
	for i, id := range rt.NetworkIDs {
		ids[i] = strconv.FormatInt(id, 10)
	}
	set, d := types.SetValueFrom(ctx, types.StringType, ids)
	diags.Append(d...)
	m.Networks = set
}

// onlyTimeoutsChanged reports whether a plan differs from state in nothing the API stores.
func onlyTimeoutsChanged(plan, state routerModel) bool {
	return plan.Name.Equal(state.Name) && plan.Description.Equal(state.Description) &&
		plan.Project.Equal(state.Project) && plan.Networks.Equal(state.Networks) &&
		plan.ExternalNetwork.Equal(state.ExternalNetwork) && plan.Enabled.Equal(state.Enabled)
}
