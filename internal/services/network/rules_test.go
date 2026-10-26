package network

import (
	"reflect"
	"testing"

	"github.com/mtn/terraform-provider-mtncloud/internal/client"
)

func s(v string) *string { return &v }
func i(v int64) *int64   { return &v }

// Shapes taken from the live captures (dev-docs/api-research.md), with test addresses.
var (
	syncedEgressV4 = client.SecurityGroupRule{ID: 1, RuleType: "custom", Direction: "egress", Protocol: "any", SourceType: "all", Destination: s("0.0.0.0/0")}
	syncedEgressV6 = client.SecurityGroupRule{ID: 2, RuleType: "custom", Direction: "egress", Protocol: "any", SourceType: "all", Destination: s("::/0")}
	apiSSH         = client.SecurityGroupRule{ID: 3, RuleType: "customRule", Name: s("tf-acc-ssh"), Direction: "ingress", Protocol: "tcp", PortRange: s("22"), SourceType: "cidr", Source: s("10.0.0.0/24")}
	apiEgressHTTPS = client.SecurityGroupRule{ID: 4, RuleType: "customRule", Name: s("https-out"), Direction: "egress", Protocol: "tcp", PortRange: s("443"), SourceType: "cidr", Source: s("192.0.2.0/24")}
	groupRule      = client.SecurityGroupRule{ID: 5, RuleType: "custom", Direction: "ingress", Protocol: "any", SourceType: "group", Source: s("198.51.100.0/24"), SourceGroupID: i(77)}
	syncedTCPAll   = client.SecurityGroupRule{ID: 6, RuleType: "custom", Name: s("22"), Direction: "ingress", Protocol: "tcp", PortRange: s("22-22"), SourceType: "cidr", Source: s("203.0.113.0/24")}
)

func TestFromAPI(t *testing.T) {
	for _, tc := range []struct {
		in   client.SecurityGroupRule
		want rule
	}{
		{syncedEgressV4, rule{Direction: "egress", Protocol: "any", CIDR: "0.0.0.0/0"}},
		{apiSSH, rule{Direction: "ingress", Protocol: "tcp", PortRange: "22", CIDR: "10.0.0.0/24", Description: "tf-acc-ssh"}},
		{apiEgressHTTPS, rule{Direction: "egress", Protocol: "tcp", PortRange: "443", CIDR: "192.0.2.0/24", Description: "https-out"}},
		{groupRule, rule{Direction: "ingress", Protocol: "any", SourceSecurityGroupID: "77"}},
		{syncedTCPAll, rule{Direction: "ingress", Protocol: "tcp", PortRange: "22", CIDR: "203.0.113.0/24", Description: "22"}},
		{client.SecurityGroupRule{Direction: "INGRESS", Protocol: "TCP", SourceType: "all", PortRange: s("*")}, rule{Direction: "ingress", Protocol: "tcp"}},
	} {
		if got := fromAPI(tc.in); got != tc.want {
			t.Errorf("fromAPI(%d) = %+v, want %+v", tc.in.ID, got, tc.want)
		}
	}
}

func TestIsDefaultEgress(t *testing.T) {
	for r, want := range map[*client.SecurityGroupRule]bool{
		&syncedEgressV4: true,
		&syncedEgressV6: true,
		&apiSSH:         false,
		&apiEgressHTTPS: false,
		&groupRule:      false,
		// A user's own allow-all egress rule, made through the API, is not a default.
		{RuleType: "customRule", Direction: "egress", Protocol: "any", SourceType: "cidr", Source: s("0.0.0.0/0")}: false,
		// Synced, but limited to one port: not a default.
		{RuleType: "custom", Direction: "egress", Protocol: "tcp", PortRange: s("443"), SourceType: "all", Destination: s("0.0.0.0/0")}: false,
	} {
		if got := isDefaultEgress(*r); got != want {
			t.Errorf("isDefaultEgress(%+v) = %v, want %v", *r, got, want)
		}
	}
}

func TestPlanRules(t *testing.T) {
	current := []client.SecurityGroupRule{syncedEgressV4, syncedEgressV6, apiSSH, apiEgressHTTPS}
	ssh := rule{Direction: "ingress", Protocol: "tcp", PortRange: "22", CIDR: "10.0.0.0/24"}
	web := rule{Direction: "ingress", Protocol: "tcp", PortRange: "443", CIDR: "0.0.0.0/0"}

	// Keep SSH (no description in config: any name matches), drop HTTPS egress, add web.
	// The default egress rules are never touched.
	del, add := planRules(current, []rule{ssh, web})
	if !reflect.DeepEqual(del, []int64{4}) || !reflect.DeepEqual(add, []rule{web}) {
		t.Errorf("got delete %v create %+v", del, add)
	}

	// Same rules, nothing to do.
	del, add = planRules(current, []rule{fromAPI(apiSSH), fromAPI(apiEgressHTTPS)})
	if len(del) != 0 || len(add) != 0 {
		t.Errorf("no-op: got delete %v create %+v", del, add)
	}

	// A changed description replaces the rule.
	renamed := ssh
	renamed.Description = "ssh from office"
	del, add = planRules(current, []rule{renamed, fromAPI(apiEgressHTTPS)})
	if !reflect.DeepEqual(del, []int64{3}) || !reflect.DeepEqual(add, []rule{renamed}) {
		t.Errorf("rename: got delete %v create %+v", del, add)
	}

	// A duplicate made outside Terraform is removed, keeping one.
	dup := apiSSH
	dup.ID = 9
	del, _ = planRules([]client.SecurityGroupRule{apiSSH, dup}, []rule{ssh})
	if !reflect.DeepEqual(del, []int64{9}) {
		t.Errorf("duplicate: got delete %v", del)
	}

	// No desired rules: every managed rule goes, the defaults stay.
	del, add = planRules(current, nil)
	if !reflect.DeepEqual(del, []int64{3, 4}) || len(add) != 0 {
		t.Errorf("empty: got delete %v create %+v", del, add)
	}
}

func TestReadRules(t *testing.T) {
	current := []client.SecurityGroupRule{syncedEgressV4, apiEgressHTTPS, apiSSH}
	prior := []rule{{Direction: "ingress", Protocol: "tcp", PortRange: "22", CIDR: "10.0.0.0/24"}} // no description
	got := readRules(current, prior)
	want := []rule{
		{Direction: "egress", Protocol: "tcp", PortRange: "443", CIDR: "192.0.2.0/24", Description: "https-out"},
		{Direction: "ingress", Protocol: "tcp", PortRange: "22", CIDR: "10.0.0.0/24"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("readRules = %+v, want %+v", got, want)
	}
}

func TestToInput(t *testing.T) {
	in := rule{Direction: "ingress", Protocol: "any", SourceSecurityGroupID: "77", Description: "from web"}.toInput()
	if in.RemoteGroupID != 77 || in.RemoteCIDR != "" || in.Name != "from web" {
		t.Errorf("toInput = %+v", in)
	}
}

func TestDefaultEgressShape(t *testing.T) {
	if !(rule{Direction: "egress", Protocol: "any", CIDR: "::/0"}).isDefaultEgressShape() {
		t.Error("::/0 egress any is the default shape")
	}
	if (rule{Direction: "egress", Protocol: "tcp", PortRange: "443", CIDR: "0.0.0.0/0"}).isDefaultEgressShape() {
		t.Error("a port-limited rule is not the default shape")
	}
}
