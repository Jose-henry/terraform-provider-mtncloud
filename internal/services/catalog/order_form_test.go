package catalog

import (
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/diag"

	"github.com/mtn/terraform-provider-mtncloud/internal/client"
)

// Plan names as the live forms list them (2026-10-31): the instance plans and PfSense's own.
func TestPlanSize(t *testing.T) {
	for _, tc := range []struct {
		name     string
		cpu, mem int64
		ok       bool
	}{
		{"G2S4", 2, 4, true},
		{"Small (G2S4)", 2, 4, true},
		{"Large (G4S8)", 4, 8, true},
		{"Ge16S48", 16, 48, true},
		{"Ge32M128", 32, 128, true},
		{"Ge96L384", 96, 384, true},
		{"Custom", 0, 0, false},
		{"XG2S4", 0, 0, false},
	} {
		cpu, mem, ok := planSize(tc.name)
		if cpu != tc.cpu || mem != tc.mem || ok != tc.ok {
			t.Errorf("planSize(%q) = %d, %d, %v; want %d, %d, %v", tc.name, cpu, mem, ok, tc.cpu, tc.mem, tc.ok)
		}
	}
}

func TestConditionMet(t *testing.T) {
	out := map[string]any{"enableAuth": true, "initReplicaSet": false, "db_engine": "mysql", "count": int64(0)}
	for _, tc := range []struct {
		cond string
		want bool
	}{
		{"", true},
		{"enableAuth", true},
		{"initReplicaSet", false},
		{"missing", false},
		{"count", false},
		{"db_engine:mysql", true},
		{"db_engine:MySQL", true},
		{"db_engine:postgres", false},
		{"db_engine:postgres|db_engine:mysql", true},
		{"initReplicaSet, enableAuth", true},
	} {
		if got := conditionMet(tc.cond, out); got != tc.want {
			t.Errorf("conditionMet(%q) = %v, want %v", tc.cond, got, tc.want)
		}
	}
}

// An appliance with a stated minimum disk: a new order must set one (the form's own default is
// smaller), but the argument isn't Required, so orders placed before stay as they are.
func TestRootDiskMinimum(t *testing.T) {
	r := orderType(t, "fortinet_firewall")
	if a := orderSchema(t, r).Schema.Attributes["root_volume_size"]; a.IsRequired() {
		t.Error("root_volume_size is Required: orders placed without it would break on upgrade")
	}
	form := &client.CatalogForm{Fields: map[string]client.FormField{"volumes": {Name: "volumes", Help: "Minimum 40GB"}}}
	for _, tc := range []struct {
		name string
		out  map[string]any
		want string
	}{
		{"left out", map[string]any{}, "Missing root disk size"},
		{"too small", map[string]any{"volumes": []any{map[string]any{"size": int64(30)}}}, "Root disk too small"},
		{"enough", map[string]any{"volumes": []any{map[string]any{"size": int64(40)}}}, ""},
	} {
		var diags diag.Diagnostics
		r.checkRules(form, tc.out, nil, &diags)
		got := ""
		if diags.HasError() {
			got = diags.Errors()[0].Summary()
		}
		if got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, got, tc.want)
		}
	}
}
