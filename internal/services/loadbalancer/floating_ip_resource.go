package loadbalancer

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-timeouts/resource/timeouts"
	"github.com/hashicorp/terraform-plugin-framework-validators/resourcevalidator"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/mtn/terraform-provider-mtncloud/internal/client"
)

var (
	_ resource.ResourceWithImportState      = (*floatingIPResource)(nil)
	_ resource.ResourceWithConfigValidators = (*floatingIPResource)(nil)
	_ resource.ResourceWithModifyPlan       = (*floatingIPResource)(nil)
)

// importedKey marks a state made by import, until the first apply: the configuration's pool is
// then the one the address came from (the cloud can't say), not a new address to swap in.
const importedKey = "imported"

func newFloatingIPResource() resource.Resource { return &floatingIPResource{} }

type floatingIPResource struct{ rbase }

func (r *floatingIPResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_load_balancer_floating_ip"
}

// known is a value set and known at apply.
func known(v types.String) bool { return !v.IsNull() && !v.IsUnknown() }

type floatingIPRes struct {
	ID               types.String   `tfsdk:"id"`
	NetworkID        types.Int64    `tfsdk:"network_id"`
	LoadBalancerID   types.String   `tfsdk:"load_balancer_id"`
	Pool             types.String   `tfsdk:"floating_ip_pool"`
	FloatingIPID     types.String   `tfsdk:"floating_ip_id"`
	FloatingAddress  types.String   `tfsdk:"floating_ip_address"`
	ReleaseOnDestroy types.Bool     `tfsdk:"release_on_destroy"`
	Address          types.String   `tfsdk:"address"`
	Timeouts         timeouts.Value `tfsdk:"timeouts"`
}

const swapNote = " Changing it swaps the address in place: the load balancer has no public IP for the few seconds " +
	"between the detach and the attach, and the old address follows `release_on_destroy`."

func (r *floatingIPResource) Schema(ctx context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "A public IP on a load balancer: a floating IP attached to its VIP, taken from a pool or one your project " +
			"already holds (by its ID or the address itself). A load balancer has at most one. The pool must be reachable " +
			"from the load balancer's network, through its router's external network; otherwise the attach is refused and " +
			"nothing is kept. Another pool or address swaps it in place.",
		Attributes: map[string]schema.Attribute{
			"id":               schema.StringAttribute{Computed: true, PlanModifiers: keepStr, Description: "The floating IP's ID."},
			"network_id":       networkAttr(),
			"load_balancer_id": lbIDAttr(),
			"floating_ip_pool": schema.StringAttribute{Optional: true,
				Description: "Take a new address from this pool, by name, for example `public-network-02` (see " +
					"`mtncloud_load_balancer_floating_ip_pools`). Give one of this, `floating_ip_id` or `floating_ip_address`." + swapNote},
			// Optional+Computed: set it to attach an address the project holds; otherwise it is the
			// ID of the address attached.
			"floating_ip_id": schema.StringAttribute{Optional: true, Computed: true, PlanModifiers: keepStr,
				Description: "Attach this address, one your project holds, by its ID (see `available_addresses` in " +
					"`mtncloud_load_balancer_floating_ip_pools`). Otherwise the ID of the address attached." + swapNote},
			"floating_ip_address": schema.StringAttribute{Optional: true,
				Description: "Attach this address, one your project holds, by the IP itself, for example `203.0.113.9`. It is checked " +
					"at plan time against the addresses the load balancer's network can use." + swapNote},
			"release_on_destroy": schema.BoolAttribute{Optional: true, Computed: true, Default: booldefault.StaticBool(true),
				Description: "On destroy or a swap, give the address back to its pool (`true`, the default), or keep it in the project " +
					"for reuse (`false`). A swap releases the old address only when this was already `true` before the apply " +
					"and still is: set to `false` in the same apply, the old address is kept. An import sets `false`, so the first plan after it shows the change to `true` unless the " +
					"configuration says `false`: until then a mistyped import ID can't release the real address."},
			"address": schema.StringAttribute{Computed: true, PlanModifiers: keepStr, Description: "The public IP."},
		},
		Blocks: map[string]schema.Block{
			"timeouts": timeouts.Block(ctx, timeouts.Opts{Create: true, Update: true, Delete: true}),
		},
	}
}

func (r *floatingIPResource) ConfigValidators(context.Context) []resource.ConfigValidator {
	return []resource.ConfigValidator{resourcevalidator.ExactlyOneOf(path.MatchRoot("floating_ip_pool"),
		path.MatchRoot("floating_ip_id"), path.MatchRoot("floating_ip_address"))}
}

// ModifyPlan checks the pool or address at plan time, as mtncloud_instance does: one the network
// can't use is an error that lists the ones it can. It plans a swap when the configuration asks for
// another address than the one attached.
func (r *floatingIPResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.Plan.Raw.IsNull() || r.client == nil {
		return
	}
	var plan, config floatingIPRes
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() || plan.NetworkID.IsUnknown() || config.Pool.IsUnknown() || config.FloatingIPID.IsUnknown() ||
		config.FloatingAddress.IsUnknown() {
		return
	}
	var state *floatingIPRes
	if !req.State.Raw.IsNull() {
		state = &floatingIPRes{}
		resp.Diagnostics.Append(req.State.Get(ctx, state)...)
	}
	imported := false
	if state != nil {
		b, d := req.Private.GetKey(ctx, importedKey)
		resp.Diagnostics.Append(d...)
		imported = len(b) > 0
	}
	if want := wanted(config, state, imported); state == nil || want {
		if err := r.check(ctx, plan.NetworkID.ValueInt64(), config, state); err != nil {
			at := "floating_ip_pool"
			if !config.FloatingAddress.IsNull() {
				at = "floating_ip_address"
			}
			resp.Diagnostics.AddAttributeError(path.Root(at), "Floating IP not usable", err.Error())
			return
		}
		if state != nil {
			// A swap: the new address and its ID are known after the attach.
			resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("id"), types.StringUnknown())...)
			resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("address"), types.StringUnknown())...)
			if config.FloatingIPID.IsNull() {
				resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("floating_ip_id"), types.StringUnknown())...)
			}
		}
	}
}

// wanted says whether the configuration asks for another address than the state's. A pool after an
// import is taken as the one the address came from.
func wanted(config floatingIPRes, state *floatingIPRes, imported bool) bool {
	if state == nil {
		return true
	}
	switch {
	case !config.FloatingAddress.IsNull():
		return config.FloatingAddress.ValueString() != state.Address.ValueString()
	case !config.FloatingIPID.IsNull():
		return config.FloatingIPID.ValueString() != state.FloatingIPID.ValueString()
	case !config.Pool.IsNull():
		if state.Pool.IsNull() {
			return !imported // from a held address to a pool: a new address
		}
		return !strings.EqualFold(config.Pool.ValueString(), state.Pool.ValueString())
	}
	return false
}

// target resolves what to attach: a held address's ID, or a pool's external ID.
func (r *floatingIPResource) target(ctx context.Context, networkID int64, m floatingIPRes) (addressID, poolID string, err error) {
	switch {
	case known(m.FloatingIPID) && m.Pool.IsNull() && m.FloatingAddress.IsNull():
		return m.FloatingIPID.ValueString(), "", nil
	case known(m.FloatingAddress):
		id, err := r.heldID(ctx, networkID, m.FloatingAddress.ValueString())
		return id, "", err
	}
	id, err := r.poolID(ctx, networkID, m.Pool.ValueString())
	return "", id, err
}

// check resolves the configured pool or address, for a plan-time error. A held ID is checked by the
// attach, as it may be the address attached now (not in the free list).
func (r *floatingIPResource) check(ctx context.Context, networkID int64, config floatingIPRes, state *floatingIPRes) error {
	switch {
	case !config.FloatingAddress.IsNull():
		if state != nil && state.Address.ValueString() == config.FloatingAddress.ValueString() {
			return nil
		}
		_, err := r.heldID(ctx, networkID, config.FloatingAddress.ValueString())
		return err
	case !config.Pool.IsNull():
		_, err := r.poolID(ctx, networkID, config.Pool.ValueString())
		return err
	}
	return nil
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

// heldID resolves an address the project holds to its ID.
func (r *floatingIPResource) heldID(ctx context.Context, networkID int64, address string) (string, error) {
	o, err := r.client.LBFloatingIPChoices(ctx, networkID)
	if err != nil {
		return "", err
	}
	free := make([]string, 0, len(o.Available))
	for _, a := range o.Available {
		if a.Address == address {
			return a.ID, nil
		}
		free = append(free, a.Address)
	}
	list := "none"
	if len(free) > 0 {
		list = strings.Join(sorted(free), ", ")
	}
	return "", fmt.Errorf("%s is not a free address your project holds that network %d can use. Free addresses: %s. "+
		"Use floating_ip_pool for a new one", address, networkID, list)
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
	addressID, poolID, err := r.target(ctx, net, plan)
	if err != nil {
		resp.Diagnostics.AddError("Floating IP not usable", err.Error())
		return
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

// Update swaps the address when the plan asks for another (its ID unknown): detach the old one,
// then attach the new. Otherwise only release_on_destroy, the pool's name or timeouts changed,
// which live in the state alone.
func (r *floatingIPResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state floatingIPRes
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.Private.SetKey(ctx, importedKey, nil)...)
	if !plan.ID.IsUnknown() {
		if state.Pool.IsNull() && known(plan.Pool) && known(state.FloatingIPID) {
			// A pool adopted after an import: only true when the address attached is from that pool.
			// Attaching what is attached changes nothing and says where the address is from.
			if !r.ready(&resp.Diagnostics) {
				return
			}
			net, lbID := plan.NetworkID.ValueInt64(), plan.LoadBalancerID.ValueString()
			poolID, err := r.poolID(ctx, net, plan.Pool.ValueString())
			var fip *client.LBFloatingIP
			if err == nil {
				fip, err = r.client.AttachLBFloatingIP(ctx, net, lbID, state.FloatingIPID.ValueString(), "")
			}
			if err == nil && fip.PoolID != "" && fip.PoolID != poolID {
				err = fmt.Errorf("the address attached isn't from pool %q, so it can't be recorded as that pool's. Nothing "+
					"was changed. Apply again to swap it for a new address from %q, or set floating_ip_id to keep it",
					plan.Pool.ValueString(), plan.Pool.ValueString())
			}
			if err != nil {
				resp.Diagnostics.AddAttributeError(path.Root("floating_ip_pool"), "Floating IP pool not adopted", err.Error()+".")
				resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
				return
			}
		}
		plan.ID, plan.FloatingIPID, plan.Address = state.ID, state.FloatingIPID, state.Address
		resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
		return
	}
	if !r.ready(&resp.Diagnostics) {
		return
	}
	timeout, d := plan.Timeouts.Update(ctx, lbTimeout)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	net, lbID := plan.NetworkID.ValueInt64(), plan.LoadBalancerID.ValueString()
	addressID, poolID, err := r.target(ctx, net, plan)
	if err != nil {
		resp.Diagnostics.AddError("Floating IP not usable", err.Error()+". The address attached is kept.")
		resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
		return
	}
	// The old address is released only when state and plan both say so: a release can't be undone,
	// so `false` set in the same apply keeps it, and so does an import's `false`.
	release := state.ReleaseOnDestroy.ValueBool() && plan.ReleaseOnDestroy.ValueBool()
	fip, err := r.swap(ctx, net, lbID, state, release, addressID, poolID, &resp.Diagnostics)
	if fip == nil {
		if err == nil { // the old address is gone and nothing replaced it: attached again by the next apply
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.Append(resp.State.Set(ctx, &state)...) // the old address is still (or again) attached
		return
	}
	plan.ID, plan.FloatingIPID, plan.Address = types.StringValue(fip.ID), types.StringValue(fip.ID), types.StringValue(fip.Address)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// swap detaches the state's address and attaches the new one. When the new one fails and the old one
// was kept in the project, the old one goes back on (state unchanged, an error returned). When it
// was released, nothing is attached: the result is nil with a nil error, and an error in diags.
func (r *floatingIPResource) swap(ctx context.Context, net int64, lbID string, state floatingIPRes, release bool, addressID, poolID string,
	diags *diag.Diagnostics) (*client.LBFloatingIP, error) {
	if err := r.client.DetachLBFloatingIP(ctx, net, lbID, release); err != nil {
		diags.AddError("Unable to detach the floating IP", err.Error()+". The address attached is kept.")
		return nil, err
	}
	fip, err := r.client.AttachLBFloatingIP(ctx, net, lbID, addressID, poolID)
	if err == nil {
		return fip, nil
	}
	err = errors.New(strings.TrimSuffix(err.Error(), "."))
	if release {
		diags.AddError("Unable to attach the new floating IP", fmt.Sprintf("%s. The old address %s was released (release_on_destroy "+
			"is true), so the load balancer has no public IP now; the next apply attaches one.", err, state.Address.ValueString()))
		return nil, nil
	}
	if _, back := r.client.AttachLBFloatingIP(ctx, net, lbID, state.FloatingIPID.ValueString(), ""); back != nil {
		diags.AddError("Unable to attach the new floating IP", fmt.Sprintf("%s. Putting the old address %s back failed too (%s); "+
			"it is still held by the project. The next apply attaches one.", err, state.Address.ValueString(), back))
		return nil, nil
	}
	diags.AddError("Unable to attach the new floating IP", fmt.Sprintf("%s. The old address %s is attached again.", err, state.Address.ValueString()))
	return nil, err
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
	resp.Diagnostics.Append(resp.Private.SetKey(ctx, importedKey, []byte(`{"v":1}`))...)
}
