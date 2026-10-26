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

type cidrValidator struct{}

func (cidrValidator) Description(context.Context) string {
	return `must be a CIDR written as its network address, for example "10.0.0.0/24"`
}

func (v cidrValidator) MarkdownDescription(ctx context.Context) string { return v.Description(ctx) }

func (cidrValidator) ValidateString(_ context.Context, req validator.StringRequest, resp *validator.StringResponse) {
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
	if m := p.Masked(); m != p {
		resp.Diagnostics.AddAttributeError(req.Path, "CIDR is not a network address",
			fmt.Sprintf("%q has host bits set. Use %q.", s, m.String()))
	} else if p.String() != s {
		// The API may store the canonical form; anything else would read back as a diff.
		resp.Diagnostics.AddAttributeError(req.Path, "CIDR is not in its usual form",
			fmt.Sprintf("Write %q as %q.", s, p.String()))
	}
}
