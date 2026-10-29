package loadbalancer

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework-timeouts/resource/timeouts"
	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64default"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/mtn/terraform-provider-mtncloud/internal/client"
	"github.com/mtn/terraform-provider-mtncloud/internal/validators"
)

var _ resource.ResourceWithImportState = (*memberResource)(nil)

func newMemberResource() resource.Resource { return &memberResource{} }

type memberResource struct{ rbase }

type memberRes struct {
	ID             types.String   `tfsdk:"id"`
	NetworkID      types.Int64    `tfsdk:"network_id"`
	LoadBalancerID types.String   `tfsdk:"load_balancer_id"`
	PoolID         types.String   `tfsdk:"pool_id"`
	Name           types.String   `tfsdk:"name"`
	Address        types.String   `tfsdk:"address"`
	Port           types.Int64    `tfsdk:"port"`
	SubnetID       types.String   `tfsdk:"subnet_id"`
	Weight         types.Int64    `tfsdk:"weight"`
	Backup         types.Bool     `tfsdk:"backup"`
	MonitorAddress types.String   `tfsdk:"monitor_address"`
	MonitorPort    types.Int64    `tfsdk:"monitor_port"`
	Enabled        types.Bool     `tfsdk:"enabled"`
	Timeouts       timeouts.Value `tfsdk:"timeouts"`
}

const memberKind = "member"

func (r *memberResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_load_balancer_member"
}

func (r *memberResource) Schema(ctx context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "One server in a load balancer pool, by its address and port. The same address and port can't be a member " +
			"of two load balancers.",
		Attributes: map[string]schema.Attribute{
			"id":               idAttr(memberKind),
			"network_id":       networkAttr(),
			"load_balancer_id": lbIDAttr(),
			"pool_id": schema.StringAttribute{Required: true, PlanModifiers: replaceStr,
				Description: "The pool, for example `mtncloud_load_balancer_pool.web.id`. Changing it replaces the member."},
			"name": nameAttr(memberKind),
			"address": schema.StringAttribute{Required: true, PlanModifiers: replaceStr, Validators: []validator.String{validators.IPv4Address()},
				Description: "The server's IP address, for example an instance's private address. Changing it replaces the member."},
			"port": schema.Int64Attribute{Required: true, PlanModifiers: replaceInt, Validators: portValid,
				Description: "The port the server listens on. Changing it replaces the member."},
			// Optional+Computed: the cloud uses the load balancer's subnet when it isn't set.
			"subnet_id": schema.StringAttribute{Optional: true, Computed: true, PlanModifiers: append(keepStr, replaceStr...),
				Description: "The cloud ID of the subnet the address is on, if not the load balancer's own. It must be your tenant's. Changing it replaces the member."},
			// Optional+Computed with a default: most members share traffic equally.
			"weight": schema.Int64Attribute{Optional: true, Computed: true, Default: int64default.StaticInt64(1),
				Validators:  []validator.Int64{int64validator.Between(0, 256)},
				Description: "Its share of the traffic against the pool's other members, 0 to 256; 0 sends it none. Defaults to `1`."},
			"backup": schema.BoolAttribute{Optional: true, Computed: true, Default: booldefault.StaticBool(false),
				Description: "Only send traffic here when every other member is down. Defaults to `false`."},
			"monitor_address": schema.StringAttribute{Optional: true, Validators: []validator.String{validators.IPv4Address()},
				Description: "Health-check this address instead of `address`. Taking it out of the configuration goes back to `address`."},
			"monitor_port": schema.Int64Attribute{Optional: true, Validators: portValid,
				Description: "Health-check this port instead of `port`. Taking it out of the configuration goes back to `port`."},
			"enabled": enabledAttr(memberKind),
		},
		Blocks: map[string]schema.Block{
			"timeouts": timeouts.Block(ctx, timeouts.Opts{Create: true, Update: true, Delete: true}),
		},
	}
}

// spec is the member to add (was nil), or the change from was.
func (r *memberResource) spec(m, was *memberRes) client.LBMemberSpec {
	s := client.LBMemberSpec{Name: strOf(m.Name), Weight: intOf(m.Weight), Backup: boolOf(m.Backup),
		MonitorAddress: strOf(m.MonitorAddress), MonitorPort: intOf(m.MonitorPort), Enabled: boolOf(m.Enabled)}
	if was != nil { // taken out of the configuration: back to the member's own address and port
		s.ClearMonitorAddress = m.MonitorAddress.IsNull() && !was.MonitorAddress.IsNull()
		s.ClearMonitorPort = m.MonitorPort.IsNull() && !was.MonitorPort.IsNull()
	}
	if was == nil {
		s.Address, s.Port = m.Address.ValueString(), m.Port.ValueInt64()
		if !m.SubnetID.IsUnknown() {
			s.SubnetID = m.SubnetID.ValueString()
		}
	}
	return s
}

func (r *memberResource) fill(m *memberRes, lb *client.LoadBalancer) (gone bool) {
	p := findPool(lb, m.PoolID.ValueString())
	if p == nil {
		return true
	}
	for _, x := range p.Members {
		if x.ID != m.ID.ValueString() {
			continue
		}
		m.Name, m.Address, m.Port = types.StringValue(x.Name), types.StringValue(x.Address), types.Int64Value(x.Port)
		m.SubnetID, m.Weight, m.Backup = types.StringValue(x.SubnetID), types.Int64Value(x.Weight), types.BoolValue(x.Backup)
		m.MonitorAddress = optStr(x.MonitorAddress)
		m.MonitorPort = types.Int64Null()
		if x.MonitorPort != 0 {
			m.MonitorPort = types.Int64Value(x.MonitorPort)
		}
		m.Enabled = types.BoolValue(x.AdminStateUp)
		return false
	}
	return true
}

func (r *memberResource) refresh(ctx context.Context, m *memberRes, st *tfsdk.State, diags *diag.Diagnostics) {
	if lb, _ := r.readLB(ctx, m.NetworkID.ValueInt64(), m.LoadBalancerID.ValueString(), diags); lb != nil && r.fill(m, lb) {
		missingAfterWrite(diags, memberKind)
	}
	diags.Append(st.Set(ctx, m)...)
}

func (r *memberResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	if !r.ready(&resp.Diagnostics) {
		return
	}
	var plan memberRes
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	timeout, d := plan.Timeouts.Create(ctx, lbTimeout)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	id, err := r.client.AddLBMember(ctx, plan.NetworkID.ValueInt64(), plan.LoadBalancerID.ValueString(), plan.PoolID.ValueString(), r.spec(&plan, nil))
	if id != "" {
		plan.ID = types.StringValue(id)
		r.refresh(ctx, &plan, &resp.State, &resp.Diagnostics)
	}
	if err != nil {
		resp.Diagnostics.AddError("Unable to add the member", err.Error())
	}
}

func (r *memberResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	if !r.ready(&resp.Diagnostics) {
		return
	}
	var state memberRes
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

func (r *memberResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	if !r.ready(&resp.Diagnostics) {
		return
	}
	var plan, state memberRes
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	timeout, d := plan.Timeouts.Update(ctx, lbTimeout)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	plan.ID = state.ID
	if err := r.client.UpdateLBMember(ctx, plan.NetworkID.ValueInt64(), plan.LoadBalancerID.ValueString(), plan.PoolID.ValueString(), plan.ID.ValueString(), r.spec(&plan, &state)); err != nil {
		resp.Diagnostics.AddError("Unable to change the member", err.Error())
		return
	}
	r.refresh(ctx, &plan, &resp.State, &resp.Diagnostics)
}

func (r *memberResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	if !r.ready(&resp.Diagnostics) {
		return
	}
	var state memberRes
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	timeout, d := state.Timeouts.Delete(ctx, lbTimeout)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if err := r.client.RemoveLBMember(ctx, state.NetworkID.ValueInt64(), state.LoadBalancerID.ValueString(), state.PoolID.ValueString(), state.ID.ValueString()); err != nil {
		resp.Diagnostics.AddError("Unable to remove the member", err.Error())
	}
}

func (r *memberResource) ImportState(_ context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	importIDs(req, resp, "<network_id>/<load_balancer_id>/<pool_id>/<member_id>", "network_id", "load_balancer_id", "pool_id", "id")
}
