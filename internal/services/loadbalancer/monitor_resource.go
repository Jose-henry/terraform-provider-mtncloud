package loadbalancer

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework-timeouts/resource/timeouts"
	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/mtn/terraform-provider-mtncloud/internal/client"
)

var (
	_ resource.ResourceWithImportState    = (*monitorResource)(nil)
	_ resource.ResourceWithValidateConfig = (*monitorResource)(nil)
)

func newMonitorResource() resource.Resource { return &monitorResource{} }

type monitorResource struct{ rbase }

type monitorRes struct {
	ID             types.String   `tfsdk:"id"`
	NetworkID      types.Int64    `tfsdk:"network_id"`
	LoadBalancerID types.String   `tfsdk:"load_balancer_id"`
	PoolID         types.String   `tfsdk:"pool_id"`
	Name           types.String   `tfsdk:"name"`
	Type           types.String   `tfsdk:"type"`
	Delay          types.Int64    `tfsdk:"delay"`
	Timeout        types.Int64    `tfsdk:"timeout"`
	MaxRetries     types.Int64    `tfsdk:"max_retries"`
	MaxRetriesDown types.Int64    `tfsdk:"max_retries_down"`
	HTTPMethod     types.String   `tfsdk:"http_method"`
	URLPath        types.String   `tfsdk:"url_path"`
	ExpectedCodes  types.String   `tfsdk:"expected_codes"`
	Enabled        types.Bool     `tfsdk:"enabled"`
	Timeouts       timeouts.Value `tfsdk:"timeouts"`
}

const monitorKind = "health monitor"

func (r *monitorResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_load_balancer_health_monitor"
}

// httpOnly is an Optional+Computed HTTP check setting: the cloud fills in its default for HTTP and
// HTTPS checks and leaves it empty for the others.
func httpOnly(desc string) schema.StringAttribute {
	return schema.StringAttribute{Optional: true, Computed: true, PlanModifiers: keepStr, Description: desc + " For `HTTP` and `HTTPS` checks." + keptWhenRemoved}
}

func (r *monitorResource) Schema(ctx context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	seconds := []validator.Int64{int64validator.AtLeast(1)}
	resp.Schema = schema.Schema{
		Description: "The health check of a load balancer pool: members that fail it get no traffic until they pass again. A pool has at most one.",
		Attributes: map[string]schema.Attribute{
			"id":               idAttr(monitorKind),
			"network_id":       networkAttr(),
			"load_balancer_id": lbIDAttr(),
			"pool_id": schema.StringAttribute{Required: true, PlanModifiers: replaceStr,
				Description: "The pool it checks, for example `mtncloud_load_balancer_pool.web.id`. Changing it replaces the health monitor."},
			"name": nameAttr(monitorKind),
			"type": schema.StringAttribute{Required: true, PlanModifiers: replaceStr,
				Validators:  []validator.String{stringvalidator.OneOf("HTTP", "HTTPS", "PING", "TCP", "TLS-HELLO", "UDP-CONNECT", "SCTP")},
				Description: "`HTTP`, `HTTPS`, `PING`, `TCP`, `TLS-HELLO`, `UDP-CONNECT` or `SCTP`. Changing it replaces the health monitor."},
			"delay":       schema.Int64Attribute{Required: true, Validators: seconds, Description: "Seconds between checks."},
			"timeout":     schema.Int64Attribute{Required: true, Validators: seconds, Description: "Seconds to wait for an answer. Must be less than `delay`."},
			"max_retries": schema.Int64Attribute{Required: true, Validators: []validator.Int64{int64validator.Between(1, 10)}, Description: "Passed checks in a row before a member counts as up, 1 to 10."},
			// Optional+Computed: the cloud's default is 3.
			"max_retries_down": schema.Int64Attribute{Optional: true, Computed: true, PlanModifiers: keepInt,
				Validators: []validator.Int64{int64validator.Between(1, 10)}, Description: "Failed checks in a row before a member counts as down, 1 to 10." + keptWhenRemoved},
			"http_method":    httpMethod(),
			"url_path":       httpOnly("The path requested, for example `/health`."),
			"expected_codes": httpOnly("The status codes that count as healthy: `200`, `200,202` or `200-204`."),
			"enabled":        enabledAttr(monitorKind),
		},
		Blocks: map[string]schema.Block{
			"timeouts": timeouts.Block(ctx, timeouts.Opts{Create: true, Update: true, Delete: true}),
		},
	}
}

// httpMethod is in capitals, as the cloud stores it, so `get` can't read back as `GET`.
func httpMethod() schema.StringAttribute {
	a := httpOnly("The request method: `GET`, `HEAD`, `POST`, `PUT`, `DELETE`, `TRACE`, `OPTIONS`, `PATCH` or `CONNECT`.")
	a.Validators = []validator.String{stringvalidator.OneOf("GET", "HEAD", "POST", "PUT", "DELETE", "TRACE", "OPTIONS", "PATCH", "CONNECT")}
	return a
}

func (r *monitorResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var m monitorRes
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if m.Delay.IsUnknown() || m.Timeout.IsUnknown() || m.Delay.IsNull() || m.Timeout.IsNull() {
		return
	}
	if m.Timeout.ValueInt64() >= m.Delay.ValueInt64() {
		resp.Diagnostics.AddAttributeError(path.Root("timeout"), "Timeout too long",
			fmt.Sprintf("timeout (%d) must be less than delay (%d): a check has to end before the next one starts.", m.Timeout.ValueInt64(), m.Delay.ValueInt64()))
	}
}

func (r *monitorResource) spec(m *monitorRes, create bool) client.LBMonitorSpec {
	s := client.LBMonitorSpec{Name: strOf(m.Name), Delay: intOf(m.Delay), Timeout: intOf(m.Timeout), MaxRetries: intOf(m.MaxRetries),
		MaxRetriesDown: intOf(m.MaxRetriesDown), HTTPMethod: strOf(m.HTTPMethod), URLPath: strOf(m.URLPath),
		ExpectedCodes: strOf(m.ExpectedCodes), Enabled: boolOf(m.Enabled)}
	if create {
		s.Type = m.Type.ValueString()
	}
	return s
}

func (r *monitorResource) fill(m *monitorRes, lb *client.LoadBalancer) (gone bool) {
	p := findPool(lb, m.PoolID.ValueString())
	if p == nil || p.HealthMonitor == nil {
		return true
	}
	h := p.HealthMonitor
	m.ID, m.Name, m.Type = types.StringValue(h.ID), types.StringValue(h.Name), types.StringValue(h.Type)
	m.Delay, m.Timeout, m.MaxRetries = types.Int64Value(h.Delay), types.Int64Value(h.Timeout), types.Int64Value(h.MaxRetries)
	m.MaxRetriesDown = types.Int64Value(h.MaxRetriesDown)
	m.HTTPMethod, m.URLPath, m.ExpectedCodes = types.StringValue(h.HTTPMethod), types.StringValue(h.URLPath), types.StringValue(h.ExpectedCodes)
	m.Enabled = types.BoolValue(h.AdminStateUp)
	return false
}

func (r *monitorResource) refresh(ctx context.Context, m *monitorRes, st *tfsdk.State, diags *diag.Diagnostics) {
	if lb, _ := r.readLB(ctx, m.NetworkID.ValueInt64(), m.LoadBalancerID.ValueString(), diags); lb != nil && r.fill(m, lb) {
		missingAfterWrite(diags, monitorKind)
	}
	diags.Append(st.Set(ctx, m)...)
}

func (r *monitorResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	if !r.ready(&resp.Diagnostics) {
		return
	}
	var plan monitorRes
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	timeout, d := plan.Timeouts.Create(ctx, lbTimeout)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	id, err := r.client.CreateLBMonitor(ctx, plan.NetworkID.ValueInt64(), plan.LoadBalancerID.ValueString(), plan.PoolID.ValueString(), r.spec(&plan, true))
	if id != "" {
		plan.ID = types.StringValue(id)
		r.refresh(ctx, &plan, &resp.State, &resp.Diagnostics)
	}
	if err != nil {
		resp.Diagnostics.AddError("Unable to create the health monitor", err.Error())
	}
}

func (r *monitorResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	if !r.ready(&resp.Diagnostics) {
		return
	}
	var state monitorRes
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

func (r *monitorResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	if !r.ready(&resp.Diagnostics) {
		return
	}
	var plan, state monitorRes
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
	if err := r.client.UpdateLBMonitor(ctx, plan.NetworkID.ValueInt64(), plan.LoadBalancerID.ValueString(), plan.PoolID.ValueString(), r.spec(&plan, false)); err != nil {
		resp.Diagnostics.AddError("Unable to change the health monitor", err.Error())
		return
	}
	r.refresh(ctx, &plan, &resp.State, &resp.Diagnostics)
}

func (r *monitorResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	if !r.ready(&resp.Diagnostics) {
		return
	}
	var state monitorRes
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	timeout, d := state.Timeouts.Delete(ctx, lbTimeout)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if err := r.client.DeleteLBMonitor(ctx, state.NetworkID.ValueInt64(), state.LoadBalancerID.ValueString(), state.PoolID.ValueString()); err != nil {
		resp.Diagnostics.AddError("Unable to delete the health monitor", err.Error())
	}
}

// ImportState takes the pool's path: a pool has at most one health monitor. Read fills in its ID.
func (r *monitorResource) ImportState(_ context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	importIDs(req, resp, "<network_id>/<load_balancer_id>/<pool_id>", "network_id", "load_balancer_id", "pool_id")
}
