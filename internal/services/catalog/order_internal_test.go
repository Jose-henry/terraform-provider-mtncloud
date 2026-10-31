package catalog

import (
	"slices"
	"testing"

	"github.com/mtn/terraform-provider-mtncloud/internal/client"
)

func TestMadeBy(t *testing.T) {
	for _, c := range []struct {
		instance, order string
		nodes, want     bool
	}{
		{"web", "web", false, true},
		{"WEB", "web", false, true},
		{"web-1", "web", true, true},
		{"web-12", "web", true, true},
		// A single-VM order never claims "web-2": another order's, or the user's own.
		{"web-2", "web", false, false},
		{"web-db", "web", true, false},
		{"web_1", "web", true, false},
		{"web-", "web", true, false},
		{"web-1a", "web", true, false},
		{"webx", "web", true, false},
		{"we", "web", true, false},
	} {
		if got := madeBy(c.instance, c.order, c.nodes); got != c.want {
			t.Errorf("madeBy(%q, %q, %v) = %v, want %v", c.instance, c.order, c.nodes, got, c.want)
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
