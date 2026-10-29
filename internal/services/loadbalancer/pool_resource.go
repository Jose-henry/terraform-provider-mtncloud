package loadbalancer

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework-timeouts/resource/timeouts"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/mtn/terraform-provider-mtncloud/internal/client"
)

var (
	_ resource.ResourceWithImportState    = (*poolResource)(nil)
	_ resource.ResourceWithValidateConfig = (*poolResource)(nil)
)

func newPoolResource() resource.Resource { return &poolResource{} }

type poolResource struct{ rbase }

type poolRes struct {
	ID                 types.String   `tfsdk:"id"`
	NetworkID          types.Int64    `tfsdk:"network_id"`
	LoadBalancerID     types.String   `tfsdk:"load_balancer_id"`
	Name               types.String   `tfsdk:"name"`
	Description        types.String   `tfsdk:"description"`
	Protocol           types.String   `tfsdk:"protocol"`
	Algorithm          types.String   `tfsdk:"algorithm"`
	SessionPersistence types.String   `tfsdk:"session_persistence"`
	CookieName         types.String   `tfsdk:"cookie_name"`
	TLSEnabled         types.Bool     `tfsdk:"tls_enabled"`
	Enabled            types.Bool     `tfsdk:"enabled"`
	Timeouts           timeouts.Value `tfsdk:"timeouts"`
}

const poolKind = "pool"

func (r *poolResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_load_balancer_pool"
}

func (r *poolResource) Schema(ctx context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "A group of servers (members) that a load balancer shares traffic across. A listener sends its traffic here " +
			"through its `default_pool_id`.",
		Attributes: map[string]schema.Attribute{
			"id":               idAttr(poolKind),
			"network_id":       networkAttr(),
			"load_balancer_id": lbIDAttr(),
			"name":             nameAttr(poolKind),
			"description":      descriptionAttr(),
			"protocol": schema.StringAttribute{Required: true, PlanModifiers: replaceStr,
				Validators:  []validator.String{stringvalidator.OneOf("HTTP", "HTTPS", "PROXY", "PROXYV2", "SCTP", "TCP", "UDP")},
				Description: "How the load balancer talks to the members: `HTTP`, `HTTPS`, `PROXY`, `PROXYV2`, `TCP`, `UDP` or `SCTP`. Changing it replaces the pool."},
			"algorithm": schema.StringAttribute{Required: true,
				Validators:  []validator.String{stringvalidator.OneOf("ROUND_ROBIN", "LEAST_CONNECTIONS", "SOURCE_IP", "SOURCE_IP_PORT")},
				Description: "How a member is picked: `ROUND_ROBIN`, `LEAST_CONNECTIONS`, `SOURCE_IP` or `SOURCE_IP_PORT`."},
			"session_persistence": schema.StringAttribute{Optional: true,
				Validators:  []validator.String{stringvalidator.OneOf("SOURCE_IP", "HTTP_COOKIE", "APP_COOKIE")},
				Description: "Keep a client on one member: `SOURCE_IP`, `HTTP_COOKIE` or `APP_COOKIE` (with `cookie_name`). Unset: none."},
			"cookie_name": schema.StringAttribute{Optional: true, Validators: []validator.String{stringvalidator.LengthBetween(1, 255)},
				Description: "The application's cookie, for `session_persistence = \"APP_COOKIE\"`."},
			// Optional+Computed with a default: most pools talk to their members unencrypted.
			"tls_enabled": schema.BoolAttribute{Optional: true, Computed: true, Default: booldefault.StaticBool(false),
				Description: "Encrypt the traffic to the members. Defaults to `false`."},
			"enabled": enabledAttr(poolKind),
		},
		Blocks: map[string]schema.Block{
			"timeouts": timeouts.Block(ctx, timeouts.Opts{Create: true, Update: true, Delete: true}),
		},
	}
}

func (r *poolResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var m poolRes
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if m.SessionPersistence.IsUnknown() || m.CookieName.IsUnknown() {
		return
	}
	app := m.SessionPersistence.ValueString() == "APP_COOKIE"
	if app && m.CookieName.IsNull() {
		resp.Diagnostics.AddAttributeError(path.Root("cookie_name"), "Missing cookie name", "session_persistence = \"APP_COOKIE\" needs cookie_name: the cookie your application sets.")
	}
	if !app && !m.CookieName.IsNull() {
		resp.Diagnostics.AddAttributeError(path.Root("cookie_name"), "Cookie name not used", "cookie_name only applies with session_persistence = \"APP_COOKIE\".")
	}
}

func (r *poolResource) spec(m *poolRes, create bool) client.LBPoolSpec {
	s := client.LBPoolSpec{Name: strOf(m.Name), Description: strOf(m.Description), Algorithm: m.Algorithm.ValueString(),
		SessionPersistence: clearOf(m.SessionPersistence), CookieName: strOf(m.CookieName),
		TLSEnabled: boolOf(m.TLSEnabled), Enabled: boolOf(m.Enabled)}
	if create {
		s.Protocol = m.Protocol.ValueString()
		if s.SessionPersistence != nil && *s.SessionPersistence == "" {
			s.SessionPersistence = nil
		}
	}
	return s
}

func findPool(lb *client.LoadBalancer, id string) *client.LBPool {
	for i := range lb.Pools {
		if lb.Pools[i].ID == id {
			return &lb.Pools[i]
		}
	}
	return nil
}

func (r *poolResource) fill(m *poolRes, lb *client.LoadBalancer) (gone bool) {
	p := findPool(lb, m.ID.ValueString())
	if p == nil {
		return true
	}
	m.Name, m.Description = types.StringValue(p.Name), types.StringValue(p.Description)
	m.Protocol, m.Algorithm = types.StringValue(p.Protocol), types.StringValue(p.Algorithm)
	m.SessionPersistence, m.CookieName = types.StringNull(), types.StringNull()
	if p.SessionPersistence != nil && p.SessionPersistence.Type != "" {
		m.SessionPersistence = types.StringValue(p.SessionPersistence.Type)
		m.CookieName = optStr(p.SessionPersistence.CookieName)
	}
	m.TLSEnabled, m.Enabled = types.BoolValue(p.TLSEnabled), types.BoolValue(p.AdminStateUp)
	return false
}

func (r *poolResource) refresh(ctx context.Context, m *poolRes, st *tfsdk.State, diags *diag.Diagnostics) {
	if lb, _ := r.readLB(ctx, m.NetworkID.ValueInt64(), m.LoadBalancerID.ValueString(), diags); lb != nil && r.fill(m, lb) {
		missingAfterWrite(diags, poolKind)
	}
	diags.Append(st.Set(ctx, m)...)
}

func (r *poolResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	if !r.ready(&resp.Diagnostics) {
		return
	}
	var plan poolRes
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	timeout, d := plan.Timeouts.Create(ctx, lbTimeout)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	id, err := r.client.CreateLBPool(ctx, plan.NetworkID.ValueInt64(), plan.LoadBalancerID.ValueString(), r.spec(&plan, true))
	if id != "" {
		plan.ID = types.StringValue(id)
		r.refresh(ctx, &plan, &resp.State, &resp.Diagnostics)
	}
	if err != nil {
		resp.Diagnostics.AddError("Unable to create the pool", err.Error())
	}
}

func (r *poolResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	if !r.ready(&resp.Diagnostics) {
		return
	}
	var state poolRes
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

func (r *poolResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	if !r.ready(&resp.Diagnostics) {
		return
	}
	var plan, state poolRes
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
	if err := r.client.UpdateLBPool(ctx, plan.NetworkID.ValueInt64(), plan.LoadBalancerID.ValueString(), plan.ID.ValueString(), r.spec(&plan, false)); err != nil {
		resp.Diagnostics.AddError("Unable to change the pool", err.Error())
		return
	}
	r.refresh(ctx, &plan, &resp.State, &resp.Diagnostics)
}

func (r *poolResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	if !r.ready(&resp.Diagnostics) {
		return
	}
	var state poolRes
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	timeout, d := state.Timeouts.Delete(ctx, lbTimeout)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if err := r.client.DeleteLBPool(ctx, state.NetworkID.ValueInt64(), state.LoadBalancerID.ValueString(), state.ID.ValueString()); err != nil {
		resp.Diagnostics.AddError("Unable to delete the pool", err.Error())
	}
}

func (r *poolResource) ImportState(_ context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	importIDs(req, resp, "<network_id>/<load_balancer_id>/<pool_id>", "network_id", "load_balancer_id", "id")
}
