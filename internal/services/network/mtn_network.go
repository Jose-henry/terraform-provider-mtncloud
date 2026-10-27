package network

import (
	"context"
	"fmt"
	"net/netip"
	"strconv"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/mtn/terraform-provider-mtncloud/internal/client"
	"github.com/mtn/terraform-provider-mtncloud/internal/normalize"
	"github.com/mtn/terraform-provider-mtncloud/internal/validators"
)

// MTN's DNS resolvers, the default for every new network (D-017). Changing them changes the plan
// of every network that doesn't set DNS: note it in the CHANGELOG.
const (
	mtnDNSPrimary   = "102.88.134.41"
	mtnDNSSecondary = "102.90.109.125"
)

var (
	_ resource.ResourceWithConfigure      = (*networkResource)(nil)
	_ resource.ResourceWithImportState    = (*networkResource)(nil)
	_ resource.ResourceWithModifyPlan     = (*networkResource)(nil)
	_ resource.ResourceWithValidateConfig = (*networkResource)(nil)
)

func newNetwork() resource.Resource { return &networkResource{} }

type networkResource struct{ base }

type networkModel struct {
	ID              types.String `tfsdk:"id"`
	Name            types.String `tfsdk:"name"`
	Description     types.String `tfsdk:"description"`
	Project         types.String `tfsdk:"project"`
	CIDR            types.String `tfsdk:"cidr"`
	Gateway         types.String `tfsdk:"gateway"`
	DNSPrimary      types.String `tfsdk:"dns_primary"`
	DNSSecondary    types.String `tfsdk:"dns_secondary"`
	DHCPEnabled     types.Bool   `tfsdk:"dhcp_enabled"`
	AllowIPOverride types.Bool   `tfsdk:"allow_ip_override"`
	IPv6            types.Object `tfsdk:"ipv6"`
	SubnetID        types.String `tfsdk:"subnet_id"`
	ExternalID      types.String `tfsdk:"external_id"`
}

type ipv6Model struct {
	CIDR         types.String `tfsdk:"cidr"`
	Gateway      types.String `tfsdk:"gateway"`
	DNSPrimary   types.String `tfsdk:"dns_primary"`
	DNSSecondary types.String `tfsdk:"dns_secondary"`
	DHCPEnabled  types.Bool   `tfsdk:"dhcp_enabled"`
}

var ipv6AttrTypes = map[string]attr.Type{
	"cidr": types.StringType, "gateway": types.StringType, "dns_primary": types.StringType,
	"dns_secondary": types.StringType, "dhcp_enabled": types.BoolType,
}

func (r *networkResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_network"
}

func (r *networkResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "An MTN Network: a private network in one project, created in OpenStack with one subnet of the same " +
			"range. Connect it to other networks or the internet with an MTN Router. It is ready as soon as it is created.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:      true,
				Description:   "Network ID.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"name": schema.StringAttribute{
				Required:    true,
				Description: "Name of the network.",
				Validators:  []validator.String{stringvalidator.LengthBetween(1, 255)},
			},
			"description": schema.StringAttribute{
				Optional:    true,
				Description: "Description of the network.",
			},
			"project": schema.StringAttribute{
				Required: true,
				Description: "Project the network is created in, by name (for example `web-prod`; the full name with the tenant " +
					"suffix also works). Changing it to another project replaces the network.",
			},
			"cidr": schema.StringAttribute{
				Required:      true,
				Description:   "IPv4 address range, for example `10.0.1.0/24`. It can't change: a new range replaces the network.",
				Validators:    []validator.String{validators.IPv4CIDR()},
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"gateway": schema.StringAttribute{
				Required:    true,
				Description: "Gateway address inside `cidr`, usually its first address (`10.0.1.1` for `10.0.1.0/24`).",
				Validators:  []validator.String{validators.IPv4Address()},
			},
			"dns_primary": schema.StringAttribute{
				// Optional+Computed only because a schema default requires Computed (D-017).
				Optional: true,
				Computed: true,
				Default:  stringdefault.StaticString(mtnDNSPrimary),
				Description: "Primary DNS server. Defaults to MTN's resolver `" + mtnDNSPrimary + "`. MTN's resolvers are " +
					"`102.88.134.41`, `102.90.109.125`, `102.89.90.112` and `120.91.123.54`.",
				Validators: []validator.String{validators.IPv4Address()},
			},
			"dns_secondary": schema.StringAttribute{
				Optional: true,
				Computed: true,
				Default:  stringdefault.StaticString(mtnDNSSecondary),
				Description: "Secondary DNS server. Defaults to MTN's resolver `" + mtnDNSSecondary + "`. Both DNS servers are always set: " +
					"a network imported without one gets MTN's resolver on the next apply.",
				Validators: []validator.String{validators.IPv4Address()},
			},
			"dhcp_enabled": schema.BoolAttribute{
				Optional:    true,
				Computed:    true,
				Default:     booldefault.StaticBool(true),
				Description: "Hand out addresses with DHCP. Defaults to `true`.",
			},
			"allow_ip_override": schema.BoolAttribute{
				Optional:    true,
				Computed:    true,
				Default:     booldefault.StaticBool(true),
				Description: "Let instances choose a static address on this network instead of DHCP. Defaults to `true`.",
			},
			"ipv6": schema.SingleNestedAttribute{
				Optional: true,
				Description: "IPv6 settings. Leave it out to keep IPv6 off (the default). Turning IPv6 on or off, or " +
					"changing its `cidr`, replaces the network.",
				Attributes: map[string]schema.Attribute{
					"cidr": schema.StringAttribute{
						Required:    true,
						Description: "IPv6 address range, for example `2001:db8:1::/64`.",
						Validators:  []validator.String{validators.IPv6CIDR()},
					},
					"gateway": schema.StringAttribute{
						Required:    true,
						Description: "IPv6 gateway address inside `cidr`.",
						Validators:  []validator.String{validators.IPv6Address()},
					},
					"dns_primary": schema.StringAttribute{
						Optional:    true,
						Description: "Primary IPv6 DNS server.",
						Validators:  []validator.String{validators.IPv6Address()},
					},
					"dns_secondary": schema.StringAttribute{
						Optional:    true,
						Description: "Secondary IPv6 DNS server.",
						Validators:  []validator.String{validators.IPv6Address()},
					},
					"dhcp_enabled": schema.BoolAttribute{
						Optional:    true,
						Computed:    true,
						Default:     booldefault.StaticBool(false),
						Description: "Hand out IPv6 addresses with DHCP. Defaults to `false`.",
					},
				},
			},
			"subnet_id": schema.StringAttribute{
				Computed:      true,
				Description:   "ID of the subnet OpenStack created with the network. An MTN Router attaches to it.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"external_id": schema.StringAttribute{
				Computed:      true,
				Description:   "OpenStack (Neutron) network ID.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
		},
	}
}

func (r *networkResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var m networkModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(gatewayInside(m.CIDR, m.Gateway, path.Root("gateway"))...)
	if m.IPv6.IsNull() || m.IPv6.IsUnknown() {
		return
	}
	var v6 ipv6Model
	resp.Diagnostics.Append(m.IPv6.As(ctx, &v6, objectAsOptions)...)
	resp.Diagnostics.Append(gatewayInside(v6.CIDR, v6.Gateway, path.Root("ipv6").AtName("gateway"))...)
}

// gatewayInside checks that a gateway is a usable address of its range. Values the validators
// refuse are left to them.
func gatewayInside(cidr, gateway types.String, at path.Path) diag.Diagnostics {
	var diags diag.Diagnostics
	if cidr.IsNull() || cidr.IsUnknown() || gateway.IsNull() || gateway.IsUnknown() {
		return diags
	}
	p, errP := netip.ParsePrefix(cidr.ValueString())
	a, errA := netip.ParseAddr(gateway.ValueString())
	if errP != nil || errA != nil || p.Addr().Is4() != a.Is4() {
		return diags
	}
	p = p.Masked()
	if !p.Contains(a) {
		diags.AddAttributeError(at, "Gateway outside the network",
			fmt.Sprintf("Gateway %s is not in %s. Use an address of the network, usually %s.", a, p, p.Addr().Next()))
	} else if a.Is4() && p.Bits() < 31 && a == lastAddr(p) {
		diags.AddAttributeError(at, "Gateway is the broadcast address",
			fmt.Sprintf("%s is the broadcast address of %s. Use a host address, usually %s.", a, p, p.Addr().Next()))
	} else if a == p.Addr() {
		diags.AddAttributeError(at, "Gateway is the network address",
			fmt.Sprintf("%s is the address of the network itself. Use a host address, usually %s.", a, p.Addr().Next()))
	}
	return diags
}

func (r *networkResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.Plan.Raw.IsNull() || r.client == nil {
		return // destroy, or validation before the provider is configured
	}
	r.planProject(ctx, req, resp)
	if req.State.Raw.IsNull() || resp.Diagnostics.HasError() {
		return
	}
	var state, plan networkModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	// IPv6 on, off, or a new IPv6 range: replace, as for the IPv4 range.
	if plan.IPv6.IsUnknown() || plan.IPv6.IsNull() != state.IPv6.IsNull() {
		if !plan.IPv6.Equal(state.IPv6) {
			resp.RequiresReplace.Append(path.Root("ipv6"))
		}
		return
	}
	if !plan.IPv6.IsNull() {
		var p6, s6 ipv6Model
		resp.Diagnostics.Append(plan.IPv6.As(ctx, &p6, objectAsOptions)...)
		resp.Diagnostics.Append(state.IPv6.As(ctx, &s6, objectAsOptions)...)
		if !p6.CIDR.Equal(s6.CIDR) {
			resp.RequiresReplace.Append(path.Root("ipv6").AtName("cidr"))
		}
	}
}

func (r *networkResource) input(ctx context.Context, m networkModel, diags *diag.Diagnostics) client.NetworkInput {
	in := client.NetworkInput{
		Name: m.Name.ValueString(), Description: m.Description.ValueString(), CIDR: m.CIDR.ValueString(),
		Gateway: m.Gateway.ValueString(), DNSPrimary: m.DNSPrimary.ValueString(), DNSSecondary: m.DNSSecondary.ValueString(),
		DHCP: m.DHCPEnabled.ValueBool(), AllowIPOverride: m.AllowIPOverride.ValueBool(),
	}
	if !m.IPv6.IsNull() && !m.IPv6.IsUnknown() {
		var v6 ipv6Model
		diags.Append(m.IPv6.As(ctx, &v6, objectAsOptions)...)
		in.IPv6 = &client.NetworkIPv6{
			CIDR: v6.CIDR.ValueString(), Gateway: v6.Gateway.ValueString(), DNSPrimary: v6.DNSPrimary.ValueString(),
			DNSSecondary: v6.DNSSecondary.ValueString(), DHCP: v6.DHCPEnabled.ValueBool(),
		}
	}
	return in
}

func (r *networkResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	if !r.ready(&resp.Diagnostics) {
		return
	}
	var plan networkModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	in := r.input(ctx, plan, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	project, err := r.client.ProjectByName(ctx, plan.Project.ValueString())
	if err != nil {
		resp.Diagnostics.AddAttributeError(path.Root("project"), "Unknown project", err.Error())
		return
	}
	in.ProjectID = project.ID

	// A network of this name already in the project is never adopted: it may not be ours.
	existing, err := r.client.FindNetwork(ctx, in.Name, project.ID)
	if err != nil {
		resp.Diagnostics.AddError("Unable to check for an existing network", err.Error())
		return
	}
	if existing != nil {
		resp.Diagnostics.AddAttributeError(path.Root("name"), "Network already exists",
			fmt.Sprintf("Project %q already has a network named %q (ID %d). Import it with "+
				"`terraform import <address> %d`, or choose another name.", plan.Project.ValueString(), in.Name, existing.ID, existing.ID))
		return
	}
	n, err := r.client.CreateNetwork(ctx, in)
	if err != nil {
		// The request may have been applied (rule 11). None existed a moment ago, so one now is ours.
		if !client.MayHaveSucceeded(err) || ctx.Err() != nil {
			resp.Diagnostics.AddError("Unable to create network", err.Error())
			return
		}
		if n, _ = r.client.FindNetwork(ctx, in.Name, project.ID); n == nil {
			resp.Diagnostics.AddError("Unable to create network", err.Error())
			return
		}
	}
	if failedStatus(n.Status) {
		// Saved so the next apply replaces it, and reported with the platform's own status.
		resp.Diagnostics.AddError("Network creation failed",
			fmt.Sprintf("MTN Cloud created network %d but reports status %q. Check the range, gateway and DNS for clashes "+
				"with other networks in the project; the next apply replaces it.", n.ID, n.Status))
	}
	if n.SubnetID == 0 {
		// The create response may not list the subnet; the network is ready, so one read has it.
		if got, err := r.client.GetNetwork(ctx, n.ID); err == nil {
			n = got
		}
	}
	resp.Diagnostics.Append(refreshNetwork(ctx, &plan, n, r.tenant(ctx, &resp.Diagnostics))...)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *networkResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	if !r.ready(&resp.Diagnostics) {
		return
	}
	var state networkModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	id, err := strconv.ParseInt(state.ID.ValueString(), 10, 64)
	if err != nil {
		resp.Diagnostics.AddError("Invalid network ID", fmt.Sprintf("%q is not a network ID: %s", state.ID.ValueString(), err))
		return
	}
	n, err := r.client.GetNetwork(ctx, id)
	if client.IsNotFound(err) {
		resp.State.RemoveResource(ctx) // deleted outside Terraform: plan a new one
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Unable to read network", err.Error())
		return
	}
	resp.Diagnostics.Append(refreshNetwork(ctx, &state, n, r.tenant(ctx, &resp.Diagnostics))...)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *networkResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	if !r.ready(&resp.Diagnostics) {
		return
	}
	var plan, state networkModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	in := r.input(ctx, plan, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	id, _ := strconv.ParseInt(state.ID.ValueString(), 10, 64)
	if err := r.client.UpdateNetwork(ctx, id, in); err != nil {
		resp.Diagnostics.AddError("Unable to update network", err.Error())
		return
	}
	n, err := r.client.GetNetwork(ctx, id)
	if err != nil {
		resp.Diagnostics.AddError("Unable to read network", err.Error())
		return
	}
	resp.Diagnostics.Append(refreshNetwork(ctx, &plan, n, r.tenant(ctx, &resp.Diagnostics))...)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *networkResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	if !r.ready(&resp.Diagnostics) {
		return
	}
	var state networkModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	id, _ := strconv.ParseInt(state.ID.ValueString(), 10, 64)
	if err := r.client.DeleteNetwork(ctx, id); err != nil {
		msg := err.Error()
		if client.IsInUse(err) {
			msg += ". Instances, a router or a load balancer are still attached to this network: remove them first " +
				"(Terraform does this itself when they are in the same configuration and reference the network)."
		}
		resp.Diagnostics.AddError("Unable to delete network", msg)
	}
}

func (r *networkResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	if _, err := strconv.ParseInt(req.ID, 10, 64); err != nil {
		resp.Diagnostics.AddError("Invalid import ID", fmt.Sprintf("Import a network by its numeric ID, for example 9301. Got %q.", req.ID))
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
}

// refreshNetwork copies the API's view of a network into the model, keeping the configuration's
// spelling where it means the same thing (project short name, unset description).
func refreshNetwork(ctx context.Context, m *networkModel, n *client.Network, tenant string) diag.Diagnostics {
	var diags diag.Diagnostics
	m.ID = types.StringValue(strconv.FormatInt(n.ID, 10))
	m.Name = types.StringValue(n.Name)
	if n.Description != "" || !m.Description.IsNull() {
		m.Description = types.StringValue(n.Description)
	}
	if m.Project.IsNull() || m.Project.IsUnknown() || !normalize.ProjectMatches(m.Project.ValueString(), n.ProjectName, tenant) {
		m.Project = types.StringValue(normalize.ProjectShortName(n.ProjectName, tenant))
	}
	m.CIDR = types.StringValue(n.CIDR)
	m.Gateway = types.StringValue(n.Gateway)
	m.DNSPrimary = types.StringValue(n.DNSPrimary)
	m.DNSSecondary = types.StringValue(n.DNSSecondary)
	m.DHCPEnabled = types.BoolValue(n.DHCP)
	m.AllowIPOverride = types.BoolValue(n.AllowIPOverride)
	m.ExternalID = types.StringValue(n.ExternalID)
	m.SubnetID = types.StringNull()
	if n.SubnetID != 0 {
		m.SubnetID = types.StringValue(strconv.FormatInt(n.SubnetID, 10))
	}
	if n.IPv6 == nil {
		m.IPv6 = types.ObjectNull(ipv6AttrTypes)
		return diags
	}
	obj, d := types.ObjectValueFrom(ctx, ipv6AttrTypes, ipv6Model{
		CIDR: types.StringValue(canonicalCIDR(n.IPv6.CIDR)), Gateway: types.StringValue(canonicalAddr(n.IPv6.Gateway)),
		DNSPrimary: optional(canonicalAddr(n.IPv6.DNSPrimary)), DNSSecondary: optional(canonicalAddr(n.IPv6.DNSSecondary)),
		DHCPEnabled: types.BoolValue(n.IPv6.DHCP),
	})
	diags.Append(d...)
	m.IPv6 = obj
	return diags
}

// failedStatus reports a network the platform marks as broken. Healthy networks report "ok".
func failedStatus(s string) bool {
	switch strings.ToLower(s) {
	case "failed", "error", "deprovisioned":
		return true
	}
	return false
}

// canonicalCIDR and canonicalAddr write an address the way the validators require it in the
// configuration ("2001:db8:1::/64"), whatever spelling the API echoes. Unparsable values are kept.
func canonicalCIDR(s string) string {
	if p, err := netip.ParsePrefix(s); err == nil {
		return p.String()
	}
	return s
}

func canonicalAddr(s string) string {
	if a, err := netip.ParseAddr(s); err == nil {
		return a.String()
	}
	return s
}

// lastAddr is the last address of an IPv4 network (its broadcast address).
func lastAddr(p netip.Prefix) netip.Addr {
	b := p.Masked().Addr().As4()
	for i := p.Bits(); i < 32; i++ {
		b[i/8] |= 1 << (7 - i%8)
	}
	return netip.AddrFrom4(b)
}
