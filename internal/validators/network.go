package validators

import (
	"context"
	"fmt"
	"net/netip"
	"strconv"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
)

// PortRange checks "22" or "8000-8080": ports 1-65535, the low port first.
func PortRange() validator.String { return portRangeValidator{} }

type portRangeValidator struct{}

func (portRangeValidator) Description(context.Context) string {
	return `must be a port ("22") or a range ("8000-8080"), ports 1-65535`
}

func (v portRangeValidator) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}

func (portRangeValidator) ValidateString(_ context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	s := req.ConfigValue.ValueString()
	lo, hi, isRange := strings.Cut(s, "-")
	if !isRange {
		hi = lo
	}
	l, errL := port(lo)
	h, errH := port(hi)
	switch {
	case errL != nil || errH != nil:
		resp.Diagnostics.AddAttributeError(req.Path, "Invalid port range",
			fmt.Sprintf("%q: use a port from 1 to 65535 (\"22\") or a range (\"8000-8080\").", s))
	case l > h:
		resp.Diagnostics.AddAttributeError(req.Path, "Invalid port range",
			fmt.Sprintf("%q: put the lower port first (\"%d-%d\").", s, h, l))
	case isRange && l == h:
		resp.Diagnostics.AddAttributeError(req.Path, "Invalid port range",
			fmt.Sprintf("%q: write a single port as %q.", s, lo))
	}
}

func port(s string) (int, error) {
	n, err := strconv.Atoi(s)
	if err != nil || n < 1 || n > 65535 {
		return 0, fmt.Errorf("bad port %q", s)
	}
	return n, nil
}

// NetworkCIDR checks an IPv4 or IPv6 CIDR written as its network address ("10.0.0.0/24", not
// "10.0.0.5/24"), so the value stored by the platform always matches the configuration.
func NetworkCIDR() validator.String { return cidrValidator{} }

// IPv4CIDR is NetworkCIDR limited to IPv4.
func IPv4CIDR() validator.String { return cidrValidator{family: 4} }

// IPv6CIDR is NetworkCIDR limited to IPv6.
func IPv6CIDR() validator.String { return cidrValidator{family: 6} }

type cidrValidator struct{ family int } // 0 = either

func (v cidrValidator) Description(context.Context) string {
	switch v.family {
	case 4:
		return `must be an IPv4 CIDR written as its network address, for example "10.0.0.0/24"`
	case 6:
		return `must be an IPv6 CIDR written as its network address, for example "2001:db8::/64"`
	}
	return `must be a CIDR written as its network address, for example "10.0.0.0/24"`
}

func (v cidrValidator) MarkdownDescription(ctx context.Context) string { return v.Description(ctx) }

func (v cidrValidator) ValidateString(_ context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	s := req.ConfigValue.ValueString()
	p, err := netip.ParsePrefix(s)
	if err != nil {
		resp.Diagnostics.AddAttributeError(req.Path, "Invalid CIDR",
			fmt.Sprintf("%q is not a CIDR. Use an address and a prefix length, for example \"10.0.0.0/24\" or \"0.0.0.0/0\".", s))
		return
	}
	if !familyOK(v.family, p.Addr()) {
		resp.Diagnostics.AddAttributeError(req.Path, "Wrong address family", fmt.Sprintf("%q: %s.", s, v.Description(context.Background())))
		return
	}
	if m := p.Masked(); m != p {
		resp.Diagnostics.AddAttributeError(req.Path, "CIDR is not a network address",
			fmt.Sprintf("%q has host bits set. Use %q.", s, m.String()))
	} else if p.String() != s {
		// The API may store the canonical form; anything else would read back as a diff.
		resp.Diagnostics.AddAttributeError(req.Path, "CIDR is not in its usual form",
			fmt.Sprintf("Write %q as %q.", s, p.String()))
	}
}

func familyOK(family int, a netip.Addr) bool {
	return family == 0 || (family == 4 && a.Is4()) || (family == 6 && a.Is6() && !a.Is4In6())
}

// IPv4Address checks an IPv4 address in its usual form ("10.0.0.1").
func IPv4Address() validator.String { return addrValidator{family: 4} }

// IPv6Address checks an IPv6 address in its usual, compressed lower-case form ("2001:db8::1").
func IPv6Address() validator.String { return addrValidator{family: 6} }

type addrValidator struct{ family int }

func (v addrValidator) Description(context.Context) string {
	if v.family == 4 {
		return `must be an IPv4 address, for example "10.0.0.1"`
	}
	return `must be an IPv6 address, for example "2001:db8::1"`
}

func (v addrValidator) MarkdownDescription(ctx context.Context) string { return v.Description(ctx) }

func (v addrValidator) ValidateString(ctx context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	s := req.ConfigValue.ValueString()
	a, err := netip.ParseAddr(s)
	if err != nil || !familyOK(v.family, a) || a.Zone() != "" {
		resp.Diagnostics.AddAttributeError(req.Path, "Invalid IP address", fmt.Sprintf("%q %s.", s, v.Description(ctx)))
		return
	}
	if a.String() != s {
		resp.Diagnostics.AddAttributeError(req.Path, "IP address is not in its usual form", fmt.Sprintf("Write %q as %q.", s, a.String()))
	}
}
