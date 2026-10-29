package loadbalancer

import (
	"context"
	"fmt"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-timeouts/resource/timeouts"
	"github.com/hashicorp/terraform-plugin-framework-validators/resourcevalidator"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var (
	_ resource.ResourceWithImportState      = (*floatingIPResource)(nil)
	_ resource.ResourceWithConfigValidators = (*floatingIPResource)(nil)
	_ resource.ResourceWithModifyPlan       = (*floatingIPResource)(nil)
)

func newFloatingIPResource() resource.Resource { return &floatingIPResource{} }

type floatingIPResource struct{ rbase }

type floatingIPRes struct {
	ID               types.String   `tfsdk:"id"`
	NetworkID        types.Int64    `tfsdk:"network_id"`
	LoadBalancerID   types.String   `tfsdk:"load_balancer_id"`
	Pool             types.String   `tfsdk:"floating_ip_pool"`
	FloatingIPID     types.String   `tfsdk:"floating_ip_id"`
	ReleaseOnDestroy types.Bool     `tfsdk:"release_on_destroy"`
	Address          types.String   `tfsdk:"address"`
	Timeouts         timeouts.Value `tfsdk:"timeouts"`
}

func (r *floatingIPResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_load_balancer_floating_ip"
}

func (r *floatingIPResource) Schema(ctx context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "A public IP on a load balancer: a floating IP attached to its VIP, taken from a pool or one your project " +
			"already holds. A load balancer has at most one. The pool must be reachable from the load balancer's network, " +
			"through its router's external network; otherwise the attach is refused and nothing is kept.",
		Attributes: map[string]schema.Attribute{
			"id":               schema.StringAttribute{Computed: true, PlanModifiers: keepStr, Description: "The floating IP's ID."},
			"network_id":       networkAttr(),
			"load_balancer_id": lbIDAttr(),
			"floating_ip_pool": schema.StringAttribute{Optional: true, PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
				Description: "Take a new address from this pool, by name, for example `public-network-02` (see " +
					"`mtncloud_load_balancer_floating_ip_pools`). Give this or `floating_ip_id`. Setting or changing it replaces the " +
					"floating IP with a new address from the pool, so after an import keep `floating_ip_id` in the configuration."},
			// Optional+Computed: set it to attach an address the project holds; otherwise it is the
			// ID of the address taken from the pool.
			"floating_ip_id": schema.StringAttribute{Optional: true, Computed: true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown(), stringplanmodifier.RequiresReplace()},
				Description: "Attach this address, one your project holds (see `available_addresses` in " +
					"`mtncloud_load_balancer_floating_ip_pools`). Give this or `floating_ip_pool`. Changing it replaces the floating IP."},
			"release_on_destroy": schema.BoolAttribute{Optional: true, Computed: true, Default: booldefault.StaticBool(true),
				Description: "On destroy, give the address back to its pool (`true`, the default), or keep it in the project for reuse " +
					"(`false`). An import sets `false`, so the first plan after it shows the change to `true` unless the " +
					"configuration says `false`: until then a mistyped import ID can't release the real address."},
			"address": schema.StringAttribute{Computed: true, PlanModifiers: keepStr, Description: "The public IP."},
		},
		Blocks: map[string]schema.Block{
			"timeouts": timeouts.Block(ctx, timeouts.Opts{Create: true, Delete: true}),
		},
	}
}

func (r *floatingIPResource) ConfigValidators(context.Context) []resource.ConfigValidator {
	return []resource.ConfigValidator{resourcevalidator.ExactlyOneOf(path.MatchRoot("floating_ip_pool"), path.MatchRoot("floating_ip_id"))}
}

// ModifyPlan checks the pool name at plan time, as mtncloud_instance does: a name the network
// can't use is an error that lists the ones it can.
func (r *floatingIPResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.Plan.Raw.IsNull() || r.client == nil {
		return
	}
	var plan floatingIPRes
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() || plan.Pool.IsNull() || plan.Pool.IsUnknown() || plan.NetworkID.IsUnknown() {
		return
	}
	if !req.State.Raw.IsNull() {
		var state floatingIPRes
		resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
		if state.Pool.Equal(plan.Pool) {
			return
		}
	}
	if _, err := r.poolID(ctx, plan.NetworkID.ValueInt64(), plan.Pool.ValueString()); err != nil {
		resp.Diagnostics.AddAttributeError(path.Root("floating_ip_pool"), "Unknown floating IP pool", err.Error())
	}
}

// poolID resolves a pool name to the ID the attach takes.
func (r *floatingIPResource) poolID(ctx context.Context, networkID int64, name string) (string, error) {
	o, err := r.client.LBFloatingIPChoices(ctx, networkID)
	if err != nil {
		return "", err
	}
	names := make([]string, 0, len(o.Pools))
	for _, p := range o.Pools {
		if strings.EqualFold(p.Name, name) {
			return p.ExternalID, nil
		}
		names = append(names, p.Name)
	}
	return "", fmt.Errorf("network %d has no floating IP pool named %q. Available pools: %s", networkID, name, strings.Join(sorted(names), ", "))
}

func (r *floatingIPResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	if !r.ready(&resp.Diagnostics) {
		return
	}
	var plan floatingIPRes
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	timeout, d := plan.Timeouts.Create(ctx, lbTimeout)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	net, lbID := plan.NetworkID.ValueInt64(), plan.LoadBalancerID.ValueString()
	// An address already there (attached in the console) is not this resource's to take, or to
	// release on destroy: the attach would answer it as if it were new. Adopt it by import.
	if row, err := r.client.LoadBalancerRow(ctx, net, lbID); err == nil && row != nil && row.FloatingIP != "" {
		resp.Diagnostics.AddError("Load balancer already has a floating IP",
			fmt.Sprintf("Load balancer %s already has floating IP %s. To manage it here, import it: terraform import <address> "+
				"%d/%s/<floating_ip_id> (see the console for its ID). To attach another, detach this one first.", lbID, row.FloatingIP, net, lbID))
		return
	}
	var addressID, poolID string
	if !plan.FloatingIPID.IsNull() && !plan.FloatingIPID.IsUnknown() {
		addressID = plan.FloatingIPID.ValueString()
	} else {
		var err error
		if poolID, err = r.poolID(ctx, net, plan.Pool.ValueString()); err != nil {
			resp.Diagnostics.AddAttributeError(path.Root("floating_ip_pool"), "Unknown floating IP pool", err.Error())
			return
		}
	}
	fip, err := r.client.AttachLBFloatingIP(ctx, net, lbID, addressID, poolID)
	if err != nil {
		resp.Diagnostics.AddError("Unable to attach the floating IP", err.Error())
		return
	}
	plan.ID, plan.FloatingIPID, plan.Address = types.StringValue(fip.ID), types.StringValue(fip.ID), types.StringValue(fip.Address)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Read takes the address from the load balancer's list row. One that is gone, or is another
// address now, was detached outside Terraform: the next apply attaches one again.
func (r *floatingIPResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	if !r.ready(&resp.Diagnostics) {
		return
	}
	var state floatingIPRes
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	row, err := r.client.LoadBalancerRow(ctx, state.NetworkID.ValueInt64(), state.LoadBalancerID.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Unable to read the load balancer's floating IP", err.Error())
		return
	}
	switch {
	case row == nil:
		resp.State.RemoveResource(ctx) // the load balancer is gone, and its address with it
		return
	case row.FloatingIP == "" && !row.FloatingIPKnown:
		resp.Diagnostics.AddWarning("Floating IP not checked",
			"The cloud could not say whether the load balancer still has its floating IP, so the state is kept as it was.")
		return
	case row.FloatingIP == "" || (!state.Address.IsNull() && state.Address.ValueString() != "" && row.FloatingIP != state.Address.ValueString()):
		resp.State.RemoveResource(ctx)
		return
	}
	state.Address = types.StringValue(row.FloatingIP)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Update only takes release_on_destroy and timeouts, which live in the state alone.
func (r *floatingIPResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state floatingIPRes
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	plan.ID, plan.FloatingIPID, plan.Address = state.ID, state.FloatingIPID, state.Address
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *floatingIPResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	if !r.ready(&resp.Diagnostics) {
		return
	}
	var state floatingIPRes
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	timeout, d := state.Timeouts.Delete(ctx, lbTimeout)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if err := r.client.DetachLBFloatingIP(ctx, state.NetworkID.ValueInt64(), state.LoadBalancerID.ValueString(), state.ReleaseOnDestroy.ValueBool()); err != nil {
		resp.Diagnostics.AddError("Unable to detach the floating IP", err.Error())
	}
}

// ImportState takes <network_id>/<load_balancer_id>/<floating_ip_id>. The ID can't be checked
// against the cloud (the list shows only the address), so an imported address is kept on destroy
// until the configuration says otherwise: a mistyped ID then can't give the real address away.
func (r *floatingIPResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	importIDs(req, resp, "<network_id>/<load_balancer_id>/<floating_ip_id>", "network_id", "load_balancer_id", "id")
	if resp.Diagnostics.HasError() {
		return
	}
	var id types.String
	resp.Diagnostics.Append(resp.State.GetAttribute(ctx, path.Root("id"), &id)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("floating_ip_id"), id)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("release_on_destroy"), false)...)
}
