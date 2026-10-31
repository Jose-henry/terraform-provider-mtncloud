package catalog

import (
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"

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

// Every catalog item that makes a VM requires its root disk (owner, 2026-10-31): forms that make it
// optional pre-fill 10 GB, smaller than the minimums their help text states. PfSense's is fixed.
func TestRootDiskRequired(t *testing.T) {
	for _, f := range orderResources() {
		r := f().(*orderResource)
		if r.spec.deprecated != "" {
			continue // the alias keeps its old arguments
		}
		a, ok := orderSchema(t, r).Schema.Attributes["root_volume_size"]
		switch {
		case r.spec.typeName == "pfsense_vpn" && ok:
			t.Error("pfsense_vpn: its disks are fixed by the form")
		case ok && !a.IsRequired():
			t.Errorf("%s: root_volume_size isn't required", r.spec.typeName)
		}
	}
}

// The minimums the forms' help text states, checked at plan, with MTN's added data disk in the docs.
func TestRootDiskMinimum(t *testing.T) {
	for _, tc := range []struct {
		typ       string
		min, data int64
	}{
		{"fortinet_firewall", 40, 0}, {"fortiweb", 70, 0}, {"fortiadc", 30, 30}, {"fortimanager", 500, 0},
		{"fortianalyzer", 250, 250}, {"sophos_firewall", 80, 40}, {"siem", 100, 60}, {"managed_streaming", 100, 60},
	} {
		r := orderType(t, tc.typ)
		if r.spec.rules.minRootGB != tc.min || r.spec.rules.dataDiskGB != tc.data {
			t.Errorf("%s: minimum %d GB, data disk %d GB; the form says %d and %d", tc.typ,
				r.spec.rules.minRootGB, r.spec.rules.dataDiskGB, tc.min, tc.data)
		}
		form := &client.CatalogForm{Fields: map[string]client.FormField{"volumes": {Name: "volumes"}}}
		for size, want := range map[int64]string{tc.min - 1: "Root disk too small", tc.min: ""} {
			var diags diag.Diagnostics
			r.checkRules(form, map[string]any{"volumes": []any{map[string]any{"size": size}}}, nil, &diags)
			got := ""
			if diags.HasError() {
				got = diags.Errors()[0].Summary()
			}
			if got != want {
				t.Errorf("%s at %d GB: got %q, want %q", tc.typ, size, got, want)
			}
		}
	}
	if d := orderType(t, "fortianalyzer").rootDiskDescription(); !strings.Contains(d, "At least 250 GB") || !strings.Contains(d, "250 GB data disk") {
		t.Errorf("description %q", d)
	}
}

// An order placed before the disk was required records the size it is now given; any other
// change orders again.
func TestInputChange(t *testing.T) {
	for _, tc := range []struct {
		name          string
		before, after attr.Value
		want          change
	}{
		{"root_volume_size", types.Int64Null(), types.Int64Value(40), recorded},
		{"root_volume_size", types.Int64Value(40), types.Int64Value(80), reorder},
		{"plan", types.StringNull(), types.StringValue("G2S4"), reorder},
		{"plan", types.StringValue("G2S4"), types.StringValue("G2S4"), noChange},
		{"extra_inputs", types.MapNull(types.StringType), types.MapValueMust(types.StringType, map[string]attr.Value{}), noChange},
	} {
		if got := inputChange(tc.name, tc.before, tc.after); got != tc.want {
			t.Errorf("%s %v -> %v: got %d, want %d", tc.name, tc.before, tc.after, got, tc.want)
		}
	}
}

// The console's own field rules, as the forms give them (2026-10-31): locked, not blank, a pattern
// (Go runs most; a look-ahead it can't is left to the console), and number limits.
func TestCheckField(t *testing.T) {
	one, three := int64(1), int64(3)
	r := orderType(t, "mysql")
	for _, tc := range []struct {
		name string
		ff   client.FormField
		v    any
		want string
	}{
		{"locked, as set", client.FormField{Name: "no_nodes", Type: "number", Locked: true, Default: float64(1)}, int64(1), ""},
		{"locked, changed", client.FormField{Name: "no_nodes", Type: "number", Locked: true, Default: float64(1)}, int64(3), "Input locked by the form"},
		{"locked flag", client.FormField{Name: "makeUserDbOwner", Type: "checkbox", Locked: true, Default: true}, false, "Input locked by the form"},
		{"locked disks the form fills", client.FormField{Name: "volumes", Type: "diskManager", Hidden: true, Locked: true, Default: "[]"}, []any{}, ""},
		{"locked name is sent", client.FormField{Name: "name", Locked: true, Default: "${userInitials}-x"}, "vpn-1", ""},
		{"blank", client.FormField{Name: "mysqlVersion", NoBlank: true}, " ", "Empty input"},
		{"pattern ok", client.FormField{Name: "dbName", Pattern: `(?i)^[a-z_][a-z0-9_\$]{0,62}$`}, "app_db", ""},
		{"pattern bad", client.FormField{Name: "dbName", Pattern: `(?i)^[a-z_][a-z0-9_\$]{0,62}$`}, "9db", "Invalid dbName"},
		{"look-ahead left to the console", client.FormField{Name: "smbPassword", Pattern: `^(?=.*[a-z]).{8,}$`}, "x", ""},
		{"in range", client.FormField{Name: "no_nodes", Min: &one, Max: &three}, int64(3), ""},
		{"out of range", client.FormField{Name: "no_nodes", Min: &one, Max: &three}, int64(4), "Out of range"},
	} {
		var diags diag.Diagnostics
		r.checkField(tc.ff, tc.v, &diags)
		got := ""
		if diags.HasError() {
			got = diags.Errors()[0].Summary()
		}
		if got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, got, tc.want)
		}
	}
	// A password that fails a pattern is never repeated in the error.
	var diags diag.Diagnostics
	r.checkField(client.FormField{Name: "pw", Type: "password", Pattern: `^.{20,}$`}, "s3cret-value", &diags)
	if !diags.HasError() || strings.Contains(diags.Errors()[0].Detail(), "s3cret-value") {
		t.Errorf("password shown or not checked: %v", diags)
	}
}

// Every order type's page carries its form's rules: each has a snapshot, read from the same item.
func TestFormSnapshots(t *testing.T) {
	for _, s := range orderSpecs() {
		snap := snapshotFor(s.typeName)
		switch {
		case snap == nil:
			t.Errorf("%s: no forms/%s.json; run TestAccCatalogForms_Snapshot", s.typeName, s.typeName)
		case !strings.EqualFold(snap.Code, s.code):
			t.Errorf("%s: snapshot of %s, the spec orders %s", s.typeName, snap.Code, s.code)
		}
	}
	min, max := int64(1), int64(3)
	f := &snapField{Required: true, Locked: true, Default: "1", Min: &min, Max: &max, Pattern: "^a$", Placeholder: "a",
		Choices: []string{"x", "y"}, Help: "Two  nodes\n aren't supported"}
	want := " Order form: required; locked (not editable); pre-filled with `1`; from 1 to 3; must match `^a$`; " +
		"for example `a`; choices: `x`, `y`. It says: \"Two nodes aren't supported.\""
	if got := f.facts(); got != want {
		t.Errorf("facts:\n got %s\nwant %s", got, want)
	}
	if got := (&snapField{Type: "select", NoBlank: true}).facts(); !strings.Contains(got, "left out, its first choice is sent") {
		t.Errorf("never-blank list: %s", got)
	}
	if got := (&snapField{RequireOn: "enableAuth", VisibleOn: "db_engine:mysql"}).facts(); got != " Order form: required when enableAuth is on; used only when db_engine is \"mysql\"." {
		t.Errorf("conditions: %s", got)
	}
}
