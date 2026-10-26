package validators

import (
	"context"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

func check(v validator.String, s string) bool {
	resp := &validator.StringResponse{}
	v.ValidateString(context.Background(), validator.StringRequest{Path: path.Root("x"), ConfigValue: types.StringValue(s)}, resp)
	return !resp.Diagnostics.HasError()
}

func TestPortRange(t *testing.T) {
	for s, ok := range map[string]bool{
		"22": true, "1": true, "65535": true, "8000-8080": true, "1-65535": true,
		"0": false, "65536": false, "22-22": false, "8080-8000": false, "": false,
		"ssh": false, "22-": false, "-22": false, "22-80-90": false, " 22": false,
	} {
		if got := check(PortRange(), s); got != ok {
			t.Errorf("PortRange(%q) valid=%v, want %v", s, got, ok)
		}
	}
	resp := &validator.StringResponse{}
	PortRange().ValidateString(context.Background(), validator.StringRequest{ConfigValue: types.StringNull()}, resp)
	if resp.Diagnostics.HasError() {
		t.Error("null must pass")
	}
}

func TestNetworkCIDR(t *testing.T) {
	for s, ok := range map[string]bool{
		"10.0.0.0/24": true, "0.0.0.0/0": true, "192.0.2.7/32": true, "::/0": true, "2001:db8::/32": true,
		"10.0.0.5/24": false, "10.0.0.0": false, "2001:DB8::/32": false, "010.0.0.0/8": false, "10.0.0.0/33": false, "foo": false, "": false,
	} {
		if got := check(NetworkCIDR(), s); got != ok {
			t.Errorf("NetworkCIDR(%q) valid=%v, want %v", s, got, ok)
		}
	}
}

func TestRegex(t *testing.T) {
	if !check(Regex(), "^web-") || check(Regex(), "(") {
		t.Error("Regex validator")
	}
}
