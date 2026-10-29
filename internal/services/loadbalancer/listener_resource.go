package loadbalancer

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework-timeouts/resource/timeouts"
	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/setvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/mtn/terraform-provider-mtncloud/internal/client"
	"github.com/mtn/terraform-provider-mtncloud/internal/validators"
)

var (
	_ resource.ResourceWithImportState    = (*listenerResource)(nil)
	_ resource.ResourceWithValidateConfig = (*listenerResource)(nil)
)

func newListenerResource() resource.Resource { return &listenerResource{} }

type listenerResource struct{ rbase }

type listenerRes struct {
	ID                   types.String   `tfsdk:"id"`
	NetworkID            types.Int64    `tfsdk:"network_id"`
	LoadBalancerID       types.String   `tfsdk:"load_balancer_id"`
	Name                 types.String   `tfsdk:"name"`
	Description          types.String   `tfsdk:"description"`
	Protocol             types.String   `tfsdk:"protocol"`
	Port                 types.Int64    `tfsdk:"port"`
	DefaultPoolID        types.String   `tfsdk:"default_pool_id"`
	ConnectionLimit      types.Int64    `tfsdk:"connection_limit"`
	AllowedCIDRs         types.Set      `tfsdk:"allowed_cidrs"`
	Enabled              types.Bool     `tfsdk:"enabled"`
	TLSCertificateRef    types.String   `tfsdk:"tls_certificate_ref"`
	SNICertificateRefs   types.Set      `tfsdk:"sni_certificate_refs"`
	ClientDataTimeout    types.Int64    `tfsdk:"client_data_timeout"`
	MemberConnectTimeout types.Int64    `tfsdk:"member_connect_timeout"`
	MemberDataTimeout    types.Int64    `tfsdk:"member_data_timeout"`
	TCPInspectTimeout    types.Int64    `tfsdk:"tcp_inspect_timeout"`
	Timeouts             timeouts.Value `tfsdk:"timeouts"`
}

// listenerProtocols are the protocols a listener may use (the plugin's LbTopology).
var listenerProtocols = []string{"HTTP", "HTTPS", "TERMINATED_HTTPS", "TCP", "UDP", "SCTP"}

func (r *listenerResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_load_balancer_listener"
}

// msTimeout is one of a listener's timeouts: Optional+Computed, because the cloud sets a default.
func msTimeout(what string) schema.Int64Attribute {
	return schema.Int64Attribute{Optional: true, Computed: true, PlanModifiers: keepInt,
		Validators:  []validator.Int64{int64validator.Between(0, 31536000000)},
		Description: what + ", in milliseconds. The cloud's default applies when it isn't set on create." + keptWhenRemoved}
}

func (r *listenerResource) Schema(ctx context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "A port a load balancer listens on, and the pool it sends that traffic to.",
		Attributes: map[string]schema.Attribute{
			"id":               idAttr(listenerKind),
			"network_id":       networkAttr(),
			"load_balancer_id": lbIDAttr(),
			"name":             nameAttr(listenerKind),
			"description":      descriptionAttr(),
			"protocol": schema.StringAttribute{Required: true, PlanModifiers: replaceStr,
				Validators:  []validator.String{stringvalidator.OneOf(listenerProtocols...)},
				Description: "`HTTP`, `HTTPS` (passed through, still encrypted), `TERMINATED_HTTPS` (decrypted here with `tls_certificate_ref`), `TCP`, `UDP` or `SCTP`. Changing it replaces the listener."},
			"port": schema.Int64Attribute{Required: true, PlanModifiers: replaceInt, Validators: portValid,
				Description: "The port, 1 to 65535. Changing it replaces the listener."},
			"default_pool_id": schema.StringAttribute{Optional: true,
				Description: "The pool that gets this listener's traffic, for example `mtncloud_load_balancer_pool.web.id`. Leave it unset for none."},
			// Optional+Computed: the cloud's default is no limit (-1).
			"connection_limit": schema.Int64Attribute{Optional: true, Computed: true, PlanModifiers: keepInt,
				Validators:  []validator.Int64{int64validator.AtLeast(-1)},
				Description: "The most connections at once; `-1` (the default) is no limit." + keptWhenRemoved},
			"allowed_cidrs": schema.SetAttribute{Optional: true, ElementType: types.StringType,
				Validators:  []validator.Set{setvalidator.SizeAtLeast(1), setvalidator.ValueStringsAre(validators.NetworkCIDR())},
				Description: "Only these source ranges may connect, for example `[\"203.0.113.0/24\"]`. Unset: anyone."},
			"enabled": enabledAttr(listenerKind),
			"tls_certificate_ref": schema.StringAttribute{Optional: true, PlanModifiers: replaceIfRemovedStr(),
				Validators: []validator.String{stringvalidator.LengthAtLeast(1)},
				Description: "For `TERMINATED_HTTPS`: the reference of a certificate uploaded in the console (the listener form shows it). " +
					"Certificates and their private keys are never sent through Terraform. Required for `TERMINATED_HTTPS`, which " +
					"must always keep a certificate: point it at another one to change it. " + removedReplaces},
			"sni_certificate_refs": schema.SetAttribute{Optional: true, ElementType: types.StringType,
				Validators:  []validator.Set{setvalidator.SizeAtLeast(1)},
				Description: "More certificate references, picked by the host name the client asks for (SNI). Taking it out of the configuration removes them."},
			"client_data_timeout":    msTimeout("How long a client may be idle"),
			"member_connect_timeout": msTimeout("How long to wait for a member to accept a connection"),
			"member_data_timeout":    msTimeout("How long a member may be idle"),
			"tcp_inspect_timeout":    msTimeout("How long to wait for more data to decide where a TCP connection goes"),
		},
		Blocks: map[string]schema.Block{
			"timeouts": timeouts.Block(ctx, timeouts.Opts{Create: true, Update: true, Delete: true}),
		},
	}
}

// ValidateConfig refuses a TERMINATED_HTTPS listener without a certificate: the cloud requires one.
func (r *listenerResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var m listenerRes
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if m.Protocol.IsUnknown() || m.TLSCertificateRef.IsUnknown() {
		return
	}
	if m.Protocol.ValueString() == "TERMINATED_HTTPS" && m.TLSCertificateRef.ValueString() == "" {
		resp.Diagnostics.AddAttributeError(path.Root("tls_certificate_ref"), "Missing certificate",
			"A TERMINATED_HTTPS listener decrypts traffic, so it needs tls_certificate_ref: a certificate uploaded in the console. "+
				"To change the certificate, point it at another one.")
	}
}

func (r *listenerResource) spec(ctx context.Context, m, was *listenerRes) client.LBListenerSpec {
	s := client.LBListenerSpec{
		Name: strOf(m.Name), Description: strOf(m.Description),
		DefaultPoolID: clearOf(m.DefaultPoolID), ConnectionLimit: intOf(m.ConnectionLimit),
		AllowedCIDRs: strsOf(ctx, m.AllowedCIDRs), Enabled: boolOf(m.Enabled),
		TLSContainerRef: strOf(m.TLSCertificateRef), SNIContainerRefs: strsOf(ctx, m.SNICertificateRefs),
		ClientDataTimeout: intOf(m.ClientDataTimeout), MemberConnectTimeout: intOf(m.MemberConnectTimeout),
		MemberDataTimeout: intOf(m.MemberDataTimeout), TCPInspectTimeout: intOf(m.TCPInspectTimeout),
	}
	if was == nil {
		s.Protocol, s.Port = m.Protocol.ValueString(), m.Port.ValueInt64()
		if s.DefaultPoolID != nil && *s.DefaultPoolID == "" {
			s.DefaultPoolID = nil
		}
		return s
	}
	// Taken out of the configuration: remove them in the cloud too.
	if m.AllowedCIDRs.IsNull() && !was.AllowedCIDRs.IsNull() {
		s.ClearAllowedCIDRs()
	}
	if m.SNICertificateRefs.IsNull() && !was.SNICertificateRefs.IsNull() {
		s.ClearSNI()
	}
	return s
}

// fill copies the listener as the cloud has it. gone reports one no longer on the load balancer.
func (r *listenerResource) fill(m *listenerRes, lb *client.LoadBalancer) (gone bool) {
	for _, l := range lb.Listeners {
		if l.ID != m.ID.ValueString() {
			continue
		}
		m.Name, m.Description = types.StringValue(l.Name), types.StringValue(l.Description)
		m.Protocol, m.Port = types.StringValue(l.Protocol), types.Int64Value(l.Port)
		m.DefaultPoolID = optStr(l.DefaultPoolID)
		m.ConnectionLimit = types.Int64Value(l.ConnectionLimit)
		m.AllowedCIDRs = setOrNull(m.AllowedCIDRs, l.AllowedCIDRs)
		m.Enabled = types.BoolValue(l.AdminStateUp)
		m.TLSCertificateRef = optStr(l.TLSContainerRef)
		m.SNICertificateRefs = setOrNull(m.SNICertificateRefs, l.SNIContainerRefs)
		m.ClientDataTimeout, m.MemberConnectTimeout = types.Int64Value(l.ClientDataTimeout), types.Int64Value(l.MemberConnTimeout)
		m.MemberDataTimeout, m.TCPInspectTimeout = types.Int64Value(l.MemberDataTimeout), types.Int64Value(l.TCPInspectTimeout)
		return false
	}
	return true
}

func (r *listenerResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	if !r.ready(&resp.Diagnostics) {
		return
	}
	var plan listenerRes
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	timeout, d := plan.Timeouts.Create(ctx, lbTimeout)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	net, lbID := plan.NetworkID.ValueInt64(), plan.LoadBalancerID.ValueString()
	id, err := r.client.CreateLBListener(ctx, net, lbID, r.spec(ctx, &plan, nil))
	if id != "" {
		plan.ID = types.StringValue(id)
		r.refresh(ctx, &plan, &resp.State, &resp.Diagnostics)
	}
	if err != nil {
		resp.Diagnostics.AddError("Unable to create the listener", err.Error())
	}
}

// refresh reads the piece once after a write, for the values the cloud filled in, and saves it.
func (r *listenerResource) refresh(ctx context.Context, m *listenerRes, st *tfsdk.State, diags *diag.Diagnostics) {
	if lb, _ := r.readLB(ctx, m.NetworkID.ValueInt64(), m.LoadBalancerID.ValueString(), diags); lb != nil && r.fill(m, lb) {
		missingAfterWrite(diags, listenerKind)
	}
	diags.Append(st.Set(ctx, m)...)
}

func (r *listenerResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	if !r.ready(&resp.Diagnostics) {
		return
	}
	var state listenerRes
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

func (r *listenerResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	if !r.ready(&resp.Diagnostics) {
		return
	}
	var plan, state listenerRes
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
	if err := r.client.UpdateLBListener(ctx, plan.NetworkID.ValueInt64(), plan.LoadBalancerID.ValueString(), plan.ID.ValueString(), r.spec(ctx, &plan, &state)); err != nil {
		resp.Diagnostics.AddError("Unable to change the listener", err.Error())
		return
	}
	r.refresh(ctx, &plan, &resp.State, &resp.Diagnostics)
}

func (r *listenerResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	if !r.ready(&resp.Diagnostics) {
		return
	}
	var state listenerRes
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	timeout, d := state.Timeouts.Delete(ctx, lbTimeout)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if err := r.client.DeleteLBListener(ctx, state.NetworkID.ValueInt64(), state.LoadBalancerID.ValueString(), state.ID.ValueString()); err != nil {
		resp.Diagnostics.AddError("Unable to delete the listener", err.Error())
	}
}

func (r *listenerResource) ImportState(_ context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	importIDs(req, resp, "<network_id>/<load_balancer_id>/<listener_id>", "network_id", "load_balancer_id", "id")
}
