package compute

import (
	"context"
	"fmt"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/mtn/terraform-provider-mtncloud/internal/client"
)

func TestLineUp(t *testing.T) {
	eq := func(a, b string) bool { return a == b }
	for _, tc := range []struct {
		cur, want     []string
		kept, removed string
	}{
		{[]string{"a", "b"}, []string{"a", "b", "c"}, "[0 1 -1]", "[]"},
		{[]string{"a", "b", "c"}, []string{"a", "c"}, "[0 2]", "[1]"},
		{[]string{"a", "b", "c"}, []string{"a"}, "[0]", "[1 2]"},
		// Moved: removed, and added again at the end (where MTN Cloud puts it).
		{[]string{"a", "b", "c"}, []string{"a", "c", "b"}, "[0 2 -1]", "[1]"},
		{nil, []string{"a"}, "[-1]", "[]"},
		{[]string{"a"}, nil, "[]", "[0]"},
	} {
		kept, removed := lineUp(tc.cur, tc.want, eq)
		if fmt.Sprint(kept) != tc.kept || fmt.Sprint(append([]int{}, removed...)) != tc.removed {
			t.Errorf("%v -> %v: kept %v removed %v, want %s %s", tc.cur, tc.want, kept, removed, tc.kept, tc.removed)
		}
	}
}

func TestBuildReconfigure(t *testing.T) {
	current := &client.Instance{PlanID: 1, Volumes: []client.Volume{
		{ID: 10, Root: true, Name: "root", Size: 20}, {ID: 11, Name: "data-1", Size: 5}, {ID: 12, Name: "data-2", Size: 5},
	}}
	cards := []client.Card{{ID: 70, NetworkID: 1, Primary: true}, {ID: 71, NetworkID: 2}, {ID: 72, NetworkID: 3}}
	root := client.VolumeSpec{Root: true, Name: "root", Size: 20}

	t.Run("nothing changes", func(t *testing.T) {
		in := client.InstanceInput{PlanID: 1, NetworkIDs: []int64{1, 2, 3},
			Volumes: []client.VolumeSpec{root, {Name: "data-1", Size: 5}, {Name: "data-2", Size: 5}}}
		if rc, _ := buildReconfigure(in, current, nil, nil); rc.needed {
			t.Errorf("a resize for no change: %+v", rc.input)
		}
		if rc, _ := buildReconfigure(in, current, cards, nil); rc.needed {
			t.Errorf("a resize for the same cards: %+v", rc.input.Interfaces)
		}
	})

	t.Run("same-size disk chosen by the plan", func(t *testing.T) {
		// Both disks are 5 GB and unnamed (as MTN Cloud leaves a disk a resize added): the plan's
		// decision says the second stays, so the first is the one deleted.
		in := client.InstanceInput{PlanID: 1, NetworkIDs: []int64{1}, Volumes: []client.VolumeSpec{root, {Size: 5}}}
		rc, err := buildReconfigure(in, current, nil, []int{1})
		if err != nil {
			t.Fatal(err)
		}
		if !rc.needed || len(rc.input.Volumes) != 2 || rc.input.Volumes[1].ID != 12 || fmt.Sprint(rc.want.RemovedVolumes) != "[11]" {
			t.Errorf("volumes %+v removed %v, want root and data-2 (12), with 11 removed", rc.input.Volumes, rc.want.RemovedVolumes)
		}
		if rc.input.Interfaces != nil {
			t.Errorf("cards sent %v when the networks don't change", rc.input.Interfaces)
		}
	})

	t.Run("stale plan", func(t *testing.T) {
		in := client.InstanceInput{PlanID: 1, Volumes: []client.VolumeSpec{root, {Size: 9}}}
		for name, kept := range map[string][]int{"gone": {5}, "count": {0, 1}, "none recorded": nil, "reused": {0, 0}} {
			if _, err := buildReconfigure(in, current, nil, kept); err == nil {
				t.Errorf("%s: no error", name)
			}
		}
		shrunk := client.InstanceInput{PlanID: 1, Volumes: []client.VolumeSpec{root, {Size: 4}, {Size: 5}}}
		if _, err := buildReconfigure(shrunk, current, nil, []int{0, 1}); err == nil {
			t.Error("shrunk: no error")
		}
	})

	t.Run("disk grown in place", func(t *testing.T) {
		// data-1 grows to 9 GB: the same volume ID is sent with the new size, data kept, and the
		// wait looks for that size. A new disk added with it goes in with its name.
		in := client.InstanceInput{PlanID: 1, Volumes: []client.VolumeSpec{root, {Name: "data-1", Size: 9},
			{Name: "data-2", Size: 5}, {Name: "data-3", Size: 2}}}
		rc, err := buildReconfigure(in, current, nil, []int{0, 1, -1})
		if err != nil {
			t.Fatal(err)
		}
		v := rc.input.Volumes
		if !rc.needed || len(v) != 4 || v[1].ID != 11 || v[1].Size != 9 || v[2].ID != 12 || v[2].Size != 5 ||
			v[3].ID > 0 || v[3].Name != "data-3" {
			t.Errorf("volumes %+v, want 11 at 9 GB, 12 as it is, and a new data-3", v)
		}
		if fmt.Sprint(rc.want.Sizes) != "map[11:9]" || len(rc.want.RemovedVolumes) != 0 || rc.want.Volumes != 4 {
			t.Errorf("want %+v, want sizes map[11:9], 4 volumes, none removed", rc.want)
		}
	})

	t.Run("cards", func(t *testing.T) {
		// eth1 (network 2) removed, network 4 added; eth0 and eth2 kept by ID.
		in := client.InstanceInput{PlanID: 1, NetworkIDs: []int64{1, 3, 4}, Volumes: []client.VolumeSpec{root, {Name: "data-1", Size: 5}, {Name: "data-2", Size: 5}}}
		rc, _ := buildReconfigure(in, current, cards, nil)
		want := "[{70 1} {72 3} { 4}]"
		if !rc.needed || fmt.Sprint(rc.input.Interfaces) != want || fmt.Sprint(rc.want.Networks) != "[1 3 4]" {
			t.Errorf("cards %v (networks %v), want %s", rc.input.Interfaces, rc.want.Networks, want)
		}
	})

	t.Run("card removed only", func(t *testing.T) {
		in := client.InstanceInput{PlanID: 1, NetworkIDs: []int64{1, 2}, Volumes: []client.VolumeSpec{root, {Name: "data-1", Size: 5}, {Name: "data-2", Size: 5}}}
		if rc, _ := buildReconfigure(in, current, cards, nil); !rc.needed || len(rc.input.Interfaces) != 2 {
			t.Errorf("needed %v cards %v, want a resize keeping eth0 and eth1", rc.needed, rc.input.Interfaces)
		}
	})
}

// Cards go in the order state lists the networks, so the apply removes the card the plan named.
func TestInStateOrder(t *testing.T) {
	cards := []client.Card{{ID: 70, NetworkID: 1, Primary: true}, {ID: 72, NetworkID: 3}, {ID: 71, NetworkID: 2}}
	got, err := inStateOrder(cards, []client.Interface{{NetworkID: 1}, {NetworkID: 2}, {NetworkID: 3}})
	if err != nil || fmt.Sprint(got) != "[{70 1 true} {71 2 false} {72 3 false}]" {
		t.Errorf("got %v, %v", got, err)
	}
	for name, ifaces := range map[string][]client.Interface{
		"a card missing":    {{NetworkID: 1}, {NetworkID: 2}},
		"another card":      {{NetworkID: 1}, {NetworkID: 2}, {NetworkID: 4}},
		"primary not first": {{NetworkID: 2}, {NetworkID: 1}, {NetworkID: 3}},
	} {
		if _, err := inStateOrder(cards, ifaces); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}

// A disk or card changed in the console after the plan stops the apply: a resize built from the
// plan's decision would leave the new disk out, and the API deletes what is left out.
func TestStaleState(t *testing.T) {
	ctx := context.Background()
	vols := func(sizes ...int64) types.List {
		var v []dataVolumeModel
		for i, s := range sizes {
			v = append(v, dataVolumeModel{Name: types.StringValue(fmt.Sprintf("data-%d", i+1)), Size: types.Int64Value(s), Datastore: types.StringNull()})
		}
		l, _ := types.ListValueFrom(ctx, dataVolumeType, v)
		return l
	}
	nets, _ := types.ListValueFrom(ctx, types.StringType, []string{"1", "2"})
	state := instanceModel{DataVolumes: vols(5), Networks: nets}
	inst := func(vols []client.Volume, nets ...int64) *client.Instance {
		in := &client.Instance{Volumes: append([]client.Volume{{ID: 10, Root: true, Size: 20}}, vols...)}
		for _, n := range nets {
			in.Interfaces = append(in.Interfaces, client.Interface{NetworkID: n})
		}
		return in
	}
	data1 := client.Volume{ID: 11, Name: "data-1", Size: 5}
	for name, tc := range map[string]struct {
		current *client.Instance
		stale   bool
	}{
		"as planned":      {inst([]client.Volume{data1}, 1, 2), false},
		"disk added":      {inst([]client.Volume{data1, {ID: 13, Name: "console", Size: 50}}, 1, 2), true},
		"disk swapped":    {inst([]client.Volume{{ID: 13, Name: "console", Size: 5}}, 1, 2), true},
		"disk grown":      {inst([]client.Volume{{ID: 11, Name: "data-1", Size: 9}}, 1, 2), true},
		"card added":      {inst([]client.Volume{data1}, 1, 2, 3), true},
		"card on another": {inst([]client.Volume{data1}, 1, 3), true},
	} {
		var diags diag.Diagnostics
		if err := staleState(ctx, tc.current, state, &diags); (err != nil) != tc.stale {
			t.Errorf("%s: error %v, want stale %v", name, err, tc.stale)
		}
	}
}
