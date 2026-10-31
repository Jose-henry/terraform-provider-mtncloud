package loadbalancer

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-framework-timeouts/resource/timeouts"
	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/boolplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64default"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/mtn/terraform-provider-mtncloud/internal/client"
	"github.com/mtn/terraform-provider-mtncloud/internal/validators"
)

var _ resource.ResourceWithImportState = (*instanceMemberResource)(nil)

func newInstanceMemberResource() resource.Resource { return &instanceMemberResource{} }

type instanceMemberResource struct{ rbase }

type instanceMemberRes struct {
	ID             types.String   `tfsdk:"id"`
	NetworkID      types.Int64    `tfsdk:"network_id"`
	LoadBalancerID types.String   `tfsdk:"load_balancer_id"`
	PoolID         types.String   `tfsdk:"pool_id"`
	InstanceID     types.Int64    `tfsdk:"instance_id"`
	PinAddress     types.String   `tfsdk:"pin_address"`
	Port           types.Int64    `tfsdk:"port"`
	Weight         types.Int64    `tfsdk:"weight"`
	Backup         types.Bool     `tfsdk:"backup"`
	Members        types.List     `tfsdk:"members"`
	Timeouts       timeouts.Value `tfsdk:"timeouts"`
}

var instanceMemberType = types.ObjectType{AttrTypes: map[string]attr.Type{"id": types.StringType, "address": types.StringType}}

const instanceMemberKind = "instance's members"

func (r *instanceMemberResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_load_balancer_instance_member"
}

func (r *instanceMemberResource) Schema(ctx context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	replaceBool := []planmodifier.Bool{boolplanmodifier.RequiresReplace()}
	resp.Schema = schema.Schema{
		Description: "An instance in a load balancer pool: every address the instance has on the load balancer's network becomes a " +
			"member, as one group. The console keeps the group in step with the instance: when it gains or loses nodes, they are " +
			"added to or removed from the pool. The instance must be one of yours with an address on the load balancer's network. " +
			"For a server outside the console, use `mtncloud_load_balancer_member`. One per instance and pool: the cloud adds and " +
			"removes an instance's members together, so a second port for the same instance needs `mtncloud_load_balancer_member`. " +
			"With `pin_address`, only that one address of the instance is added (\"pin one node\"): the console checks it is your " +
			"instance's, then never adds to or removes it as the instance scales.",
		Attributes: map[string]schema.Attribute{
			"id":               schema.StringAttribute{Computed: true, PlanModifiers: keepStr, Description: "The instance's ID, as a string."},
			"network_id":       networkAttr(),
			"load_balancer_id": lbIDAttr(),
			"pool_id": schema.StringAttribute{Required: true, PlanModifiers: replaceStr,
				Description: "The pool, for example `mtncloud_load_balancer_pool.web.id`. Changing it replaces the members."},
			"instance_id": schema.Int64Attribute{Required: true, PlanModifiers: replaceInt,
				Description: "The instance, for example `mtncloud_instance.web.id`. A new instance can take up to a minute to be accepted. " +
					"Changing it replaces the members."},
			"pin_address": schema.StringAttribute{Optional: true, PlanModifiers: replaceStr, Validators: []validator.String{validators.IPv4Address()},
				Description: "One of the instance's addresses on the load balancer's network, to add only that node. It stays a member " +
					"when the instance scales, and other nodes aren't added. Leave it out for every node. Changing it replaces the member."},
			"port": schema.Int64Attribute{Required: true, PlanModifiers: replaceInt, Validators: portValid,
				Description: "The port the instance listens on. Changing it replaces the members."},
			// Optional+Computed with a default: most members share traffic equally.
			"weight": schema.Int64Attribute{Optional: true, Computed: true, Default: int64default.StaticInt64(1), PlanModifiers: replaceInt,
				Validators:  []validator.Int64{int64validator.Between(0, 256)},
				Description: "Each member's share of the traffic, 0 to 256; 0 sends them none. Defaults to `1`. Changing it replaces the members."},
			"backup": schema.BoolAttribute{Optional: true, Computed: true, Default: booldefault.StaticBool(false), PlanModifiers: replaceBool,
				Description: "Only send traffic here when every other member is down. Defaults to `false`. Changing it replaces the members."},
			"members": schema.ListNestedAttribute{Computed: true,
				Description: "The instance's members now, sorted by address. The list follows the instance as it scales.",
				NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
					"id":      schema.StringAttribute{Computed: true, Description: "Member ID."},
					"address": schema.StringAttribute{Computed: true, Description: "The node's address."},
				}},
			},
		},
		Blocks: map[string]schema.Block{
			"timeouts": timeouts.Block(ctx, timeouts.Opts{Create: true, Delete: true}),
		},
	}
}

// fill reads the instance's group into m. Gone when the pool has none of its members.
func (r *instanceMemberResource) fill(m *instanceMemberRes, lb *client.LoadBalancer) (gone bool) {
	p := findPool(lb, m.PoolID.ValueString())
	if p == nil {
		return true
	}
	ms := client.InstanceMembers(p, m.InstanceID.ValueInt64())
	if pin := m.PinAddress.ValueString(); pin != "" {
		ms = nil
		if x := client.PinnedMember(p, pin, m.Port.ValueInt64()); x != nil {
			ms = []client.LBMember{*x}
		}
	}
	if len(ms) == 0 {
		return true
	}
	flat := flattenMembers(ms) // sorted by address
	elems := make([]attr.Value, 0, len(flat))
	for _, x := range flat {
		elems = append(elems, types.ObjectValueMust(instanceMemberType.AttrTypes, map[string]attr.Value{
			"id": types.StringValue(x.ID), "address": types.StringValue(x.Address)}))
	}
	m.Members = types.ListValueMust(instanceMemberType, elems)
	// The group was added with one port, weight and backup; its first member shows them.
	m.Port, m.Weight, m.Backup = types.Int64Value(flat[0].Port), types.Int64Value(flat[0].Weight), types.BoolValue(flat[0].Backup)
	return false
}

func (r *instanceMemberResource) refresh(ctx context.Context, m *instanceMemberRes, st *tfsdk.State, diags *diag.Diagnostics) {
	if lb, _ := r.readLB(ctx, m.NetworkID.ValueInt64(), m.LoadBalancerID.ValueString(), diags); lb != nil && r.fill(m, lb) {
		missingAfterWrite(diags, instanceMemberKind)
	}
	diags.Append(st.Set(ctx, m)...)
}

func (r *instanceMemberResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	if !r.ready(&resp.Diagnostics) {
		return
	}
	var plan instanceMemberRes
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	timeout, d := plan.Timeouts.Create(ctx, lbTimeout)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	// Members tagged for this instance are already there: removing either resource would remove both,
	// and each would read the other's port. Adopt them by import instead.
	if lb, _ := r.readLB(ctx, plan.NetworkID.ValueInt64(), plan.LoadBalancerID.ValueString(), &resp.Diagnostics); lb != nil {
		p := findPool(lb, plan.PoolID.ValueString())
		pin := plan.PinAddress.ValueString()
		switch {
		case p == nil:
		case pin == "" && len(client.InstanceMembers(p, plan.InstanceID.ValueInt64())) > 0:
			resp.Diagnostics.AddAttributeError(path.Root("instance_id"), "Instance already in the pool",
				fmt.Sprintf("Instance %d already has members in pool %s, and the cloud removes an instance's members together, so "+
					"only one resource can own them. To manage them here, import them: terraform import <address> %d/%s/%s/%d. "+
					"For another port, use mtncloud_load_balancer_member.", plan.InstanceID.ValueInt64(), plan.PoolID.ValueString(),
					plan.NetworkID.ValueInt64(), plan.LoadBalancerID.ValueString(), plan.PoolID.ValueString(), plan.InstanceID.ValueInt64()))
		case pin != "" && client.PinnedMember(p, pin, plan.Port.ValueInt64()) != nil:
			// A pinned node is one member at address:port: adding it again would be refused or doubled.
			resp.Diagnostics.AddAttributeError(path.Root("pin_address"), "Address already in the pool",
				fmt.Sprintf("Pool %s already has a member at %s:%d. To manage it here, import it: terraform import <address> %d/%s/%s/%d@%s.",
					plan.PoolID.ValueString(), pin, plan.Port.ValueInt64(), plan.NetworkID.ValueInt64(),
					plan.LoadBalancerID.ValueString(), plan.PoolID.ValueString(), plan.InstanceID.ValueInt64(), pin))
		}
	}
	if resp.Diagnostics.HasError() {
		return
	}
	err := r.client.AddLBInstanceMembers(ctx, plan.NetworkID.ValueInt64(), plan.LoadBalancerID.ValueString(), plan.PoolID.ValueString(),
		client.LBInstanceMemberSpec{InstanceID: plan.InstanceID.ValueInt64(), PinAddress: plan.PinAddress.ValueString(), Port: plan.Port.ValueInt64(), Weight: plan.Weight.ValueInt64(), Backup: plan.Backup.ValueBool()})
	if err != nil {
		// The group may have been added before the error (the load balancer went to ERROR): keep
		// it in state when the pool shows it, so the next apply doesn't add it twice.
		resp.Diagnostics.AddError("Unable to add the instance to the pool", err.Error())
		// On a context of its own: the create's may be what ran out.
		rctx, rcancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Minute)
		defer rcancel()
		if lb, _ := r.readLB(rctx, plan.NetworkID.ValueInt64(), plan.LoadBalancerID.ValueString(), &diag.Diagnostics{}); lb != nil && !r.fill(&plan, lb) {
			plan.ID = types.StringValue(instanceMemberID(plan))
			resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
		}
		return
	}
	plan.ID = types.StringValue(instanceMemberID(plan))
	r.refresh(ctx, &plan, &resp.State, &resp.Diagnostics)
}

func (r *instanceMemberResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	if !r.ready(&resp.Diagnostics) {
		return
	}
	var state instanceMemberRes
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	lb, gone := r.readLB(ctx, state.NetworkID.ValueInt64(), state.LoadBalancerID.ValueString(), &resp.Diagnostics)
	if gone || (lb != nil && r.fill(&state, lb)) {
		resp.State.RemoveResource(ctx)
		return
	}
	if lb != nil {
		resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
	}
}

// Update only takes new timeouts: every argument replaces the members.
func (r *instanceMemberResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state instanceMemberRes
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	plan.ID, plan.Members = state.ID, state.Members
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *instanceMemberResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	if !r.ready(&resp.Diagnostics) {
		return
	}
	var state instanceMemberRes
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	timeout, d := state.Timeouts.Delete(ctx, lbTimeout)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if state.PinAddress.ValueString() != "" {
		// A pinned node is an ordinary member: removed by its ID, as read on the last refresh.
		for _, id := range memberIDs(ctx, state.Members) {
			if err := r.client.RemoveLBMember(ctx, state.NetworkID.ValueInt64(), state.LoadBalancerID.ValueString(), state.PoolID.ValueString(), id); err != nil {
				resp.Diagnostics.AddError("Unable to remove the pinned node from the pool", err.Error())
			}
		}
		return
	}
	if err := r.client.RemoveLBInstanceMembers(ctx, state.NetworkID.ValueInt64(), state.LoadBalancerID.ValueString(), state.PoolID.ValueString(), state.InstanceID.ValueInt64()); err != nil {
		resp.Diagnostics.AddError("Unable to remove the instance from the pool", err.Error())
	}
}

// ImportState takes <network_id>/<load_balancer_id>/<pool_id>/<instance_id>, or
// .../<instance_id>@<pin_address> for one pinned node.
func (r *instanceMemberResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	importIDs(req, resp, "<network_id>/<load_balancer_id>/<pool_id>/<instance_id>[@<pin_address>]", "network_id", "load_balancer_id", "pool_id", "id")
	if resp.Diagnostics.HasError() {
		return
	}
	var id types.String
	resp.Diagnostics.Append(resp.State.GetAttribute(ctx, path.Root("id"), &id)...)
	last, pin, pinned := strings.Cut(id.ValueString(), "@")
	inst, err := strconv.ParseInt(last, 10, 64)
	if err != nil || (pinned && net.ParseIP(pin) == nil) {
		resp.Diagnostics.AddError("Invalid import ID", "The last part must be the instance's numeric ID, or <instance_id>@<address> for a pinned node, got "+
			strconv.Quote(id.ValueString())+".")
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("instance_id"), inst)...)
	if pinned {
		resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("pin_address"), pin)...)
	}
}

// instanceMemberID is the instance's ID, with "@<address>" for a pinned node.
func instanceMemberID(m instanceMemberRes) string {
	id := strconv.FormatInt(m.InstanceID.ValueInt64(), 10)
	if pin := m.PinAddress.ValueString(); pin != "" {
		id += "@" + pin
	}
	return id
}

// memberIDs are the IDs in a members list.
func memberIDs(ctx context.Context, l types.List) []string {
	var ms []struct {
		ID      types.String `tfsdk:"id"`
		Address types.String `tfsdk:"address"`
	}
	_ = l.ElementsAs(ctx, &ms, false)
	out := make([]string, 0, len(ms))
	for _, m := range ms {
		out = append(out, m.ID.ValueString())
	}
	return out
}
