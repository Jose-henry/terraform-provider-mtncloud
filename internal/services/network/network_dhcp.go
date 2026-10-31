package network

import (
	"context"
	"fmt"
	"net/netip"
	"sort"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/setvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/mtn/terraform-provider-mtncloud/internal/validators"
)

// A network's DHCP allocation and host routes (B-010). The network type's own fields
// (config.dhcpRange, config.hostRoutes, placeholder "x.x.x.x,x.x.x.x") were proved live on a QA
// network, 2026-10-31: one "<start>,<end>" range, inside the network and in order; host routes one
// "<destination CIDR>,<next hop>" per line; "" clears either. A range MTN Cloud's network service
// refuses (outside the network, or two of them) is still answered "success", and the network turns
// "failed": so the range is checked at plan time, and the status after every write.

type dhcpRangeModel struct {
	Start types.String `tfsdk:"start"`
	End   types.String `tfsdk:"end"`
}

type hostRouteModel struct {
	Destination types.String `tfsdk:"destination"`
	NextHop     types.String `tfsdk:"next_hop"`
}

var (
	dhcpRangeAttrTypes = map[string]attr.Type{"start": types.StringType, "end": types.StringType}
	hostRouteAttrTypes = map[string]attr.Type{"destination": types.StringType, "next_hop": types.StringType}
)

func dhcpAttributes() map[string]schema.Attribute {
	return map[string]schema.Attribute{
		"dhcp_range": schema.SingleNestedAttribute{
			Optional: true,
			Description: "The addresses DHCP hands out, one range inside `cidr` that leaves out the gateway, for example " +
				"`{ start = \"10.0.1.50\", end = \"10.0.1.99\" }`. Left out, the whole network. Changed in place; removing it " +
				"goes back to the whole network.",
			Attributes: map[string]schema.Attribute{
				"start": schema.StringAttribute{Required: true, Description: "First address.", Validators: []validator.String{validators.IPv4Address()}},
				"end":   schema.StringAttribute{Required: true, Description: "Last address.", Validators: []validator.String{validators.IPv4Address()}},
			},
		},
		"host_routes": schema.SetNestedAttribute{
			Optional: true,
			Description: "Static routes DHCP gives the instances on this network, for example to reach another network " +
				"through an instance that isn't the gateway. Changed in place. Leave it out for none.",
			Validators: []validator.Set{setvalidator.SizeAtLeast(1)},
			NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
				"destination": schema.StringAttribute{Required: true, Description: "Network the route leads to, for example `10.99.0.0/16`.",
					Validators: []validator.String{validators.IPv4CIDR()}},
				"next_hop": schema.StringAttribute{Required: true, Description: "Address the traffic goes through.",
					Validators: []validator.String{validators.IPv4Address()}},
			}},
		},
	}
}

// checkDHCPRange checks a range at plan time: inside the network, in order, and without the
// gateway, which MTN Cloud's network service refuses after answering "success".
func checkDHCPRange(ctx context.Context, m networkModel) diag.Diagnostics {
	var diags diag.Diagnostics
	if m.DHCPRange.IsNull() || m.DHCPRange.IsUnknown() || m.CIDR.IsUnknown() || m.Gateway.IsUnknown() {
		return diags
	}
	var r dhcpRangeModel
	if diags.Append(m.DHCPRange.As(ctx, &r, objectAsOptions)...); diags.HasError() || r.Start.IsUnknown() || r.End.IsUnknown() {
		return diags
	}
	p, err := netip.ParsePrefix(m.CIDR.ValueString())
	start, errS := netip.ParseAddr(r.Start.ValueString())
	end, errE := netip.ParseAddr(r.End.ValueString())
	if err != nil || errS != nil || errE != nil {
		return diags // the validators report it
	}
	p = p.Masked()
	at := path.Root("dhcp_range")
	for _, a := range []netip.Addr{start, end} {
		if !p.Contains(a) || a == p.Addr() || (p.Bits() < 31 && a == lastAddr(p)) {
			diags.AddAttributeError(at, "DHCP range outside the network",
				fmt.Sprintf("%s is not a host address of %s. The range must be inside the network.", a, p))
			return diags
		}
	}
	if end.Less(start) {
		diags.AddAttributeError(at, "DHCP range in the wrong order", fmt.Sprintf("start %s comes after end %s.", start, end))
		return diags
	}
	if gw, err := netip.ParseAddr(m.Gateway.ValueString()); err == nil && !gw.Less(start) && !end.Less(gw) {
		diags.AddAttributeError(at, "DHCP range holds the gateway",
			fmt.Sprintf("The range %s-%s includes the gateway %s. Start it after the gateway, or end it before.", start, end, gw))
	}
	return diags
}

// dhcpWire is what the model sends: the range and the routes (sorted, one per line), "" to clear.
func dhcpWire(ctx context.Context, m networkModel, diags *diag.Diagnostics) (dhcpRange, hostRoutes string) {
	if !m.DHCPRange.IsNull() && !m.DHCPRange.IsUnknown() {
		var r dhcpRangeModel
		diags.Append(m.DHCPRange.As(ctx, &r, objectAsOptions)...)
		dhcpRange = r.Start.ValueString() + "," + r.End.ValueString()
	}
	if !m.HostRoutes.IsNull() && !m.HostRoutes.IsUnknown() {
		var routes []hostRouteModel
		diags.Append(m.HostRoutes.ElementsAs(ctx, &routes, false)...)
		lines := make([]string, 0, len(routes))
		for _, h := range routes {
			lines = append(lines, canonicalCIDR(h.Destination.ValueString())+","+h.NextHop.ValueString())
		}
		sort.Strings(lines)
		hostRoutes = strings.Join(lines, "\n")
	}
	return dhcpRange, hostRoutes
}

// refreshDHCP reads the range and routes back. A value this provider can't parse (set in the
// console in another form) is kept out of state, with a warning, rather than guessed.
func refreshDHCP(ctx context.Context, m *networkModel, dhcpRange, hostRoutes string) diag.Diagnostics {
	var diags diag.Diagnostics
	m.DHCPRange = types.ObjectNull(dhcpRangeAttrTypes)
	if dhcpRange != "" {
		start, end, ok := strings.Cut(dhcpRange, ",")
		if ok && !strings.ContainsAny(end, ",\n") {
			obj, d := types.ObjectValueFrom(ctx, dhcpRangeAttrTypes, dhcpRangeModel{
				Start: types.StringValue(canonicalAddr(strings.TrimSpace(start))), End: types.StringValue(canonicalAddr(strings.TrimSpace(end))),
			})
			diags.Append(d...)
			m.DHCPRange = obj
		} else {
			diags.AddWarning("DHCP range not understood", fmt.Sprintf("The network's DHCP allocation is %q, not one \"<start>,<end>\" range; it is left out of state.", dhcpRange))
		}
	}
	var routes []hostRouteModel
	for _, line := range strings.Split(hostRoutes, "\n") {
		if line = strings.TrimSpace(line); line == "" {
			continue
		}
		dest, hop, ok := strings.Cut(line, ",")
		if !ok {
			diags.AddWarning("Host route not understood", fmt.Sprintf("Host route %q isn't \"<destination>,<next hop>\"; it is left out of state.", line))
			continue
		}
		routes = append(routes, hostRouteModel{Destination: types.StringValue(canonicalCIDR(strings.TrimSpace(dest))), NextHop: types.StringValue(canonicalAddr(strings.TrimSpace(hop)))})
	}
	elem := types.ObjectType{AttrTypes: hostRouteAttrTypes}
	if len(routes) == 0 {
		m.HostRoutes = types.SetNull(elem)
		return diags
	}
	set, d := types.SetValueFrom(ctx, elem, routes)
	diags.Append(d...)
	m.HostRoutes = set
	return diags
}
