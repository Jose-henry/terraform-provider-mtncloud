package catalog

import (
	"slices"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/mtn/terraform-provider-mtncloud/internal/client"
)

func TestMadeBy(t *testing.T) {
	for _, c := range []struct {
		instance, order string
		want            bool
	}{
		{"web", "web", true},
		{"WEB", "web", true},
		// Never "web-2" or "web-1": another order's, or the user's own. An app's nodes are named from
		// MTN's template and found through the app (B-018 #2).
		{"web-2", "web", false},
		{"web-1", "web", false},
		{"web-db", "web", false},
		{"webx", "web", false},
		{"we", "web", false},
	} {
		if got := madeBy(c.instance, c.order); got != c.want {
			t.Errorf("madeBy(%q, %q) = %v, want %v", c.instance, c.order, got, c.want)
		}
	}
}

func TestDeletePlan(t *testing.T) {
	for _, c := range []struct {
		name                 string
		apps                 []client.App
		ids                  []int64
		managed              bool
		appID                int64
		wantApps, wantDirect []int64
		wantGone             []int64
		wantDetach           [][2]int64
	}{
		{"instances only", nil, []int64{1, 2}, false, 0, nil, []int64{1, 2}, []int64{1, 2}, nil},
		{"an app of only the order's instances", []client.App{{ID: 90, Name: "x", InstanceIDs: []int64{1, 2}}},
			[]int64{1, 2}, false, 0, []int64{90}, nil, []int64{1, 2}, nil},
		// The app also holds someone else's instance 7: it stays, and 1 goes on its own.
		{"a shared app", []client.App{{ID: 90, Name: "x", InstanceIDs: []int64{1, 7}}},
			[]int64{1}, false, 0, nil, []int64{1}, []int64{1}, nil},
		{"another app", []client.App{{ID: 91, Name: "db", InstanceIDs: []int64{7}}}, []int64{1}, false, 0, nil, []int64{1}, []int64{1}, nil},
		// A managed service's own app goes with its instances, found by name.
		{"managed app by name", []client.App{{ID: 92, Name: "K8S", InstanceIDs: []int64{3, 4, 5}}},
			[]int64{3, 4, 5}, true, 0, []int64{92}, nil, []int64{3, 4, 5}, nil},
		// An instance added to the order's app later isn't the order's: out of the app first, kept (B-028 catch 2).
		{"managed app with an added instance", []client.App{{ID: 92, Name: "k8s", InstanceIDs: []int64{3, 4, 9}}},
			[]int64{3, 4}, true, 0, []int64{92}, nil, []int64{3, 4}, [][2]int64{{92, 9}}},
		// The saved app, renamed since: still the order's (B-028 catch 1). An app with the order's name isn't.
		{"saved app, renamed", []client.App{{ID: 94, Name: "renamed", InstanceIDs: []int64{3}}, {ID: 95, Name: "k8s", InstanceIDs: []int64{8}}},
			[]int64{3}, true, 94, []int64{94}, nil, []int64{3}, nil},
		{"same name, not managed", []client.App{{ID: 92, Name: "k8s", InstanceIDs: []int64{3, 8}}},
			[]int64{3}, false, 0, nil, []int64{3}, []int64{3}, nil},
		// An app of that name holding none of the order's instances: another project's, or the user's.
		{"managed name, none in common", []client.App{{ID: 93, Name: "k8s", InstanceIDs: []int64{8}}},
			[]int64{3}, true, 0, nil, []int64{3}, []int64{3}, nil},
		{"nothing", nil, nil, true, 0, nil, nil, nil, nil},
	} {
		got := deletePlan(c.apps, c.ids, "k8s", c.managed, c.appID)
		if !slices.Equal(got.apps, c.wantApps) || !slices.Equal(got.instances, c.wantDirect) || !slices.Equal(got.gone, c.wantGone) ||
			!slices.Equal(got.detach, c.wantDetach) {
			t.Errorf("%s: got %+v, want apps %v, instances %v, gone %v, detach %v", c.name, got, c.wantApps, c.wantDirect, c.wantGone, c.wantDetach)
		}
	}
}

func TestPoolNetwork(t *testing.T) {
	primary := map[string]int64{"": 1, "master_": 2, "worker_": 3}
	for attr, want := range map[string]int64{
		"floating_ip_pool":        1,
		"master_floating_ip_pool": 2,
		"worker_floating_ip_pool": 3,
		"edge_floating_ip_pool":   0,
	} {
		if got := poolNetwork(attr, primary); got != want {
			t.Errorf("%s: network %d, want %d", attr, got, want)
		}
	}
}

// The orders whose workflow builds the VM through a Heat stack, names it itself and reports it:
// exactly those whose MTN script does (`Sophos/sophosheat.sh`, `Fortinet/FortiADCheat.sh`). One
// missing is found by the order's name, which the VM never has, so destroy deletes nothing.
func TestHeatSpecs(t *testing.T) {
	var got []string
	for _, s := range orderSpecs() {
		if s.heat {
			got = append(got, s.typeName)
		}
	}
	slices.Sort(got)
	if want := []string{"fortiadc", "sophos_firewall"}; !slices.Equal(got, want) {
		t.Errorf("heat orders = %v, want %v", got, want)
	}
}

// A refused change never shows a sensitive input's old value: a secret, or extra_inputs, where a
// form field can be a password. A plain input shows the value to set back.
func TestRefuseInputHidesSecrets(t *testing.T) {
	r := &orderResource{spec: orderSpec{item: "MTN MySQL"}}
	held := types.MapValueMust(types.StringType, map[string]attr.Value{"dbRootPassword": types.StringValue("hunter2")})
	for _, c := range []struct {
		name   string
		f      field
		before attr.Value
		shown  bool
	}{
		{"extra_inputs", field{attr: "extra_inputs", kind: kExtra}, held, false},
		{"database_password", field{attr: "database_password", kind: kSecret}, types.StringValue("hunter2"), false},
		{"version", field{attr: "version", kind: kText}, types.StringValue("hunter2"), true},
	} {
		var d diag.Diagnostics
		r.refuseInput(c.name, c.f, c.before, &d)
		if !d.HasError() {
			t.Fatalf("%s: no error", c.name)
		}
		if got := strings.Contains(d[0].Detail(), "hunter2"); got != c.shown {
			t.Errorf("%s: value shown = %v, want %v: %s", c.name, got, c.shown, d[0].Detail())
		}
	}
}

// No argument sets a field the order form hides or locks: the console never lets a user set one,
// so neither does the provider (owner, 2026-11-02). Checked against the form snapshots.
func TestOrderSpecs_NoHiddenOrLockedArguments(t *testing.T) {
	for _, s := range orderSpecs() {
		snap := snapshotFor(s.typeName)
		if snap == nil {
			t.Errorf("%s: no form snapshot", s.typeName)
			continue
		}
		for _, f := range s.fields {
			if sf := snap.field(f.key); sf != nil && (sf.Hidden || sf.Locked) {
				t.Errorf("%s: argument %s sets the form's %s field %q", s.typeName, f.attr, map[bool]string{true: "hidden", false: "locked"}[sf.Hidden], f.key)
			}
		}
	}
}
