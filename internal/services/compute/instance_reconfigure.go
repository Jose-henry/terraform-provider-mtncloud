package compute

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/mtn/terraform-provider-mtncloud/internal/client"
)

// Network cards and data volumes change in place (owner, 2026-10-31): cards and disks can be added,
// and any card but the primary (eth0) and any disk but the root removed. A resize takes the full
// lists: kept ones with their IDs, new ones without, and ones left out are removed; a removed disk
// is deleted with its data (live QA 2026-10-31).

// lineUp lines the wanted list up against the current one, in order: a current item is kept when it
// is the next one wanted, and removed otherwise; wanted items left over are new. MTN Cloud adds a
// card or disk after the existing ones, so this is also the order it reports them in, and the next
// plan is empty. kept[i] is the index in cur of want[i], or -1 for a new one.
func lineUp[C, W any](cur []C, want []W, same func(C, W) bool) (kept, removed []int) {
	kept = make([]int, len(want))
	for i := range kept {
		kept[i] = -1
	}
	j := 0
	for i, c := range cur {
		if j < len(want) && same(c, want[j]) {
			kept[j] = i
			j++
		} else {
			removed = append(removed, i)
		}
	}
	return kept, removed
}

// planNetworks decides what a change to networks does. The first network is the primary card,
// which MTN Cloud can't change, nor the cards of an instance with several servers: those are refused
// (owner, 2026-10-31: nothing is replaced to fake a change). A value known only at apply is checked
// when Terraform plans again then.
func planNetworks(ctx context.Context, plan, state instanceModel, resp *resource.ModifyPlanResponse) {
	if plan.Networks.Equal(state.Networks) || plan.Networks.IsUnknown() {
		return
	}
	var want []types.String
	resp.Diagnostics.Append(plan.Networks.ElementsAs(ctx, &want, false)...)
	cur := stringsOf(ctx, state.Networks, &resp.Diagnostics)
	if len(want) == 0 || len(cur) == 0 || want[0].IsUnknown() {
		return
	}
	if want[0].ValueString() != cur[0] {
		resp.Diagnostics.AddAttributeError(path.Root("networks").AtListIndex(0), "The first network can't change",
			fmt.Sprintf("The first network is the instance's primary card (eth0), and MTN Cloud can't move it. Keep %q "+
				"first; other networks can be added and removed after it.", cur[0]))
		return
	}
	if servers(state.ScaleFactor) > 1 || servers(plan.ScaleFactor) > 1 {
		resp.Diagnostics.AddAttributeError(path.Root("networks"), "Networks can't change with several servers",
			"MTN Cloud changes network cards only on an instance with one server. Keep networks as they are, or bring "+
				"scale_factor to 1 first, in its own apply.")
		return
	}
	_, removed := lineUp(cur[1:], want[1:], func(c string, w types.String) bool {
		return !w.IsUnknown() && w.ValueString() == c
	})
	if len(removed) == 0 {
		return
	}
	nets := make([]string, len(removed))
	for i, k := range removed {
		nets[i] = cur[1+k]
	}
	resp.Diagnostics.AddAttributeWarning(path.Root("networks"), "Network cards will be removed",
		fmt.Sprintf("This apply removes the instance's cards on networks %s, and their addresses. A network moved "+
			"within the list is removed and added again at the end, with a new address.", strings.Join(nets, ", ")))
}

// servers is the number of servers a scale_factor asks for: one when it is unset or unknown.
func servers(v types.Int64) int64 {
	if !known64(v) {
		return 1
	}
	return v.ValueInt64()
}

// planVolumes decides what a change to data_volume does, all in place (owner, 2026-10-31: no disk
// is ever deleted to make a change). A disk keeps its identity: one named in the configuration is
// that disk wherever it is listed, and an unnamed one is the disk at its position, as Terraform shows
// list blocks. A kept disk can grow, never shrink, and can't change datastore; a block removed
// deletes that disk only (the plan warns which), and one added is a new disk, named by the provider
// so every disk has a name. The decision is kept in private state, so the apply does exactly this.
func planVolumes(ctx context.Context, req resource.ModifyPlanRequest, planDV, stateDV types.List, resp *resource.ModifyPlanResponse) {
	if planDV.IsUnknown() {
		resp.Diagnostics.AddAttributeError(path.Root("data_volume"), "Disks must be known at plan time",
			"The data_volume blocks of an existing instance depend on a value known only at apply, so the plan can't say which "+
				"disks stay and which are deleted. Apply what they depend on first (terraform apply -target), then plan again.")
		return
	}
	var cfgList types.List
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("data_volume"), &cfgList)...)
	want, cur, cfg := volumesOf(ctx, planDV, &resp.Diagnostics), volumesOf(ctx, stateDV, &resp.Diagnostics),
		volumesOf(ctx, cfgList, &resp.Diagnostics)
	named := func(i int) types.String {
		if i < len(cfg) {
			return cfg[i].Name
		}
		return types.StringNull()
	}
	for i, w := range want {
		if w.Datastore.IsUnknown() || named(i).IsUnknown() {
			// Which disk this is can't be known until apply, and a disk is never guessed away.
			resp.Diagnostics.AddAttributeError(path.Root("data_volume").AtListIndex(i), "Disk must be known at plan time",
				"This disk's name or datastore is known only at apply, so the plan can't say which disk it is. Apply what it "+
					"depends on first (terraform apply -target), then plan again.")
			return
		}
	}
	kept := make([]int, len(want))
	claimed := make([]bool, len(cur))
	for i := range want { // named disks first, wherever they are listed
		kept[i] = -1
		if n := named(i); !n.IsNull() {
			for k, c := range cur {
				if !claimed[k] && c.Name.ValueString() == n.ValueString() {
					kept[i], claimed[k] = k, true
					break
				}
			}
		}
	}
	// A name that matches no disk, on a block whose position holds a disk not named elsewhere, is
	// a rename: MTN Cloud keeps a disk's name (live QA 2026-10-31), and a new disk there would delete
	// the old one, so it is refused.
	for i, k := range kept {
		if n := named(i); k < 0 && !n.IsNull() && i < len(cur) && !claimed[i] && !namedElsewhere(cur[i].Name.ValueString(), cfg) {
			resp.Diagnostics.AddAttributeError(path.Root("data_volume").AtListIndex(i).AtName("name"), "A disk can't be renamed",
				fmt.Sprintf("The disk at this position is named %q, and MTN Cloud can't rename a disk. Set name = %q, or leave "+
					"name out.", cur[i].Name.ValueString(), cur[i].Name.ValueString()))
		}
	}
	if resp.Diagnostics.HasError() {
		return
	}
	for i := range want { // then unnamed ones by position
		if named(i).IsNull() && i < len(cur) && !claimed[i] && !namedElsewhere(cur[i].Name.ValueString(), cfg) {
			kept[i], claimed[i] = i, true
		}
	}
	at := func(i int, attr string) path.Path { return path.Root("data_volume").AtListIndex(i).AtName(attr) }
	last, added := -1, -1
	for i, k := range kept {
		if k < 0 {
			if added < 0 {
				added = i
			}
			continue
		}
		if added >= 0 {
			resp.Diagnostics.AddAttributeError(path.Root("data_volume").AtListIndex(added), "New disks go last",
				fmt.Sprintf("A new disk is listed before %s, which exists. MTN Cloud adds a disk after the ones there are: list new "+
					"disks after the existing ones.", diskName(cur[k].Name.ValueString(), k)))
			return
		}
		c, w := cur[k], want[i]
		switch {
		case k < last:
			resp.Diagnostics.AddAttributeError(at(i, "name"), "Disks keep their order",
				fmt.Sprintf("Disk %s is listed before %s, but was made after it. MTN Cloud keeps disks in the order they were "+
					"made: list them in that order.", diskName(c.Name.ValueString(), k), diskName(cur[last].Name.ValueString(), last)))
		case !sameName(w.Datastore, c.Datastore):
			resp.Diagnostics.AddAttributeError(at(i, "datastore"), "A disk can't change datastore",
				fmt.Sprintf("Disk %s is on datastore %q, and MTN Cloud can't move a disk. Keep it there; for a disk on another "+
					"datastore, add a new data_volume.", diskName(c.Name.ValueString(), k), c.Datastore.ValueString()))
		case known64(w.Size) && w.Size.ValueInt64() < c.Size.ValueInt64():
			resp.Diagnostics.AddAttributeError(at(i, "size"), "A disk can't shrink",
				fmt.Sprintf("Disk %s has %d GB, and a disk can only grow. Keep it at %d GB or more.",
					diskName(c.Name.ValueString(), k), c.Size.ValueInt64(), c.Size.ValueInt64()))
		}
		last = k
	}
	if resp.Diagnostics.HasError() {
		return
	}
	if b, err := json.Marshal(kept); err == nil {
		resp.Diagnostics.Append(resp.Private.SetKey(ctx, keptDisksKey, b)...)
	}
	// Names: a kept disk keeps its own, and a new one gets the next free data-<n> (MTN Cloud leaves a
	// disk added by a resize unnamed, live QA 2026-10-31).
	taken := map[string]bool{}
	for _, c := range cur {
		taken[c.Name.ValueString()] = true
	}
	for i := range want {
		if n := named(i); !n.IsNull() {
			taken[n.ValueString()] = true
		}
	}
	next := 1
	for i, k := range kept {
		switch {
		case k >= 0:
			resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, at(i, "name"), cur[k].Name)...)
		case named(i).IsNull():
			for taken["data-"+strconv.Itoa(next)] {
				next++
			}
			name := "data-" + strconv.Itoa(next)
			taken[name] = true
			resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, at(i, "name"), types.StringValue(name))...)
		}
	}
	var gone []string
	for k, c := range cur {
		if !claimed[k] {
			gone = append(gone, fmt.Sprintf("%s (%d GB)", diskName(c.Name.ValueString(), k), c.Size.ValueInt64()))
		}
	}
	if len(gone) > 0 {
		resp.Diagnostics.AddAttributeWarning(path.Root("data_volume"), "Data volumes will be deleted",
			fmt.Sprintf("This apply deletes %s, and everything on them, as their data_volume blocks are gone. An unnamed "+
				"block is the disk at its position: to remove a disk from the middle of the list, give the disks names.",
				strings.Join(gone, ", ")))
	}
}

// namedElsewhere reports whether the configuration names this disk in another block.
func namedElsewhere(name string, cfg []dataVolumeModel) bool {
	for _, c := range cfg {
		if known(c.Name) && c.Name.ValueString() == name {
			return true
		}
	}
	return false
}

// keptDisksKey is the private plan key holding, for each planned data disk, the index of the current
// disk it keeps (-1 for a new one).
const keptDisksKey = "data_volumes_kept"

// keptDisks reads the plan's decision; nil when the plan made none.
func keptDisks(b []byte) []int {
	var kept []int
	if len(b) == 0 || json.Unmarshal(b, &kept) != nil {
		return nil
	}
	return kept
}

// diskName names a disk in a message; disks added by a resize have no name.
func diskName(name string, i int) string {
	if name != "" {
		return fmt.Sprintf("%q", name)
	}
	return fmt.Sprintf("data disk %d (unnamed)", i+1)
}

// reconfigure is the resize an update sends, and what it should leave.
type reconfigure struct {
	input client.ResizeInput
	want  client.Reconfigured
	// needed is false when nothing the resize carries changes.
	needed bool
}

// buildReconfigure turns the planned volumes and networks into a resize. cards is nil when the
// networks don't change. kept is the plan's decision on disks (planVolumes); the current disks are
// in the order state lists them, so its indices point at them.
func buildReconfigure(in client.InstanceInput, current *client.Instance, cards []client.Card, kept []int) (reconfigure, error) {
	out := reconfigure{
		input: client.ResizeInput{PlanID: in.PlanID},
		want:  client.Reconfigured{PlanID: in.PlanID},
	}
	out.needed = current.PlanID != in.PlanID
	var curData []client.Volume
	for _, v := range current.Volumes {
		if v.Root {
			out.input.Volumes = append(out.input.Volumes, spec(v))
		} else {
			curData = append(curData, v)
		}
	}
	wantData := in.Volumes
	if len(wantData) > 0 && wantData[0].Root {
		wantData = wantData[1:]
	}
	if kept == nil && len(wantData) == len(curData) {
		kept = make([]int, len(curData)) // no decision recorded: the same disks
		for i := range kept {
			kept[i] = i
		}
	}
	if len(kept) != len(wantData) {
		return out, fmt.Errorf("the plan kept %d of %d disks; plan again", len(kept), len(wantData))
	}
	used := make([]bool, len(curData))
	for i, k := range kept {
		if k < 0 {
			continue
		}
		if k >= len(curData) || used[k] || wantData[i].Size < curData[k].Size {
			return out, fmt.Errorf("the instance's disks changed since the plan; refresh and plan again")
		}
		used[k] = true
	}
	var removed []int
	for k := range curData {
		if !used[k] {
			removed = append(removed, k)
		}
	}
	for i, w := range wantData {
		if k := kept[i]; k >= 0 {
			v := spec(curData[k])
			if w.Size > v.Size { // grown in place, data kept
				v.Size = w.Size
				out.needed = true
				if out.want.Sizes == nil {
					out.want.Sizes = map[int64]int64{}
				}
				out.want.Sizes[v.ID] = w.Size
			}
			out.input.Volumes = append(out.input.Volumes, v)
			continue
		}
		out.input.Volumes = append(out.input.Volumes, w)
		out.needed = true
	}
	for _, k := range removed {
		out.want.RemovedVolumes = append(out.want.RemovedVolumes, curData[k].ID)
		out.needed = true
	}
	out.want.Volumes = len(out.input.Volumes)

	if cards != nil && len(in.NetworkIDs) > 0 {
		nics := []client.Interface{{ID: strconv.FormatInt(cards[0].ID, 10), NetworkID: cards[0].NetworkID}}
		keptCards, gone := lineUp(cards[1:], in.NetworkIDs[1:], func(c client.Card, w int64) bool { return c.NetworkID == w })
		for i, w := range in.NetworkIDs[1:] {
			nic := client.Interface{NetworkID: w}
			if k := keptCards[i]; k >= 0 {
				nic.ID = strconv.FormatInt(cards[1+k].ID, 10)
			} else {
				out.needed = true
			}
			nics = append(nics, nic)
		}
		if len(gone) > 0 {
			out.needed = true
		}
		out.input.Interfaces = nics
		out.want.Networks = append([]int64{cards[0].NetworkID}, in.NetworkIDs[1:]...)
	}
	return out, nil
}

// inStateOrder puts the cards in the order the instance lists its networks, which state and the
// plan's warning used, so the apply removes the cards the plan named. They must be the same cards.
func inStateOrder(cards []client.Card, ifaces []client.Interface) ([]client.Card, error) {
	if len(cards) != len(ifaces) {
		return nil, fmt.Errorf("the instance lists %d networks but its server has %d cards; refresh and plan again", len(ifaces), len(cards))
	}
	out := make([]client.Card, 0, len(cards))
	for _, n := range ifaces {
		i := slices.IndexFunc(cards, func(c client.Card) bool { return c.NetworkID == n.NetworkID })
		if i < 0 {
			return nil, fmt.Errorf("network %d has no card on the instance's server; refresh and plan again", n.NetworkID)
		}
		out = append(out, cards[i])
		cards = slices.Delete(slices.Clone(cards), i, i+1)
	}
	if !out[0].Primary {
		return nil, fmt.Errorf("the first network isn't on the primary card; refresh and plan again")
	}
	return out, nil
}

// staleState reports whether the instance's disks or cards differ from the state the plan was
// made from (changed in the console since): the plan's decision would then send the wrong lists, and
// a disk left out of a resize is deleted.
func staleState(ctx context.Context, current *client.Instance, state instanceModel, diags *diag.Diagnostics) error {
	if err := staleDisks(ctx, current, state.DataVolumes, diags); err != nil {
		return err
	}
	nets := stringsOf(ctx, state.Networks, diags)
	changed := len(nets) != len(current.Interfaces)
	for i := 0; !changed && i < len(nets); i++ {
		changed = nets[i] != strconv.FormatInt(current.Interfaces[i].NetworkID, 10)
	}
	if changed {
		return fmt.Errorf("the instance's network cards changed since the plan; nothing was changed. Refresh and plan again")
	}
	return nil
}

// staleDisks reports whether the instance's data disks differ from the state's (stateDV), which
// the plan's decision on them was made from.
func staleDisks(ctx context.Context, current *client.Instance, stateDV types.List, diags *diag.Diagnostics) error {
	var data []client.Volume
	for _, v := range current.Volumes {
		if !v.Root {
			data = append(data, v)
		}
	}
	was := volumesOf(ctx, stateDV, diags)
	changed := len(data) != len(was)
	for i := 0; !changed && i < len(data); i++ {
		changed = data[i].Size != was[i].Size.ValueInt64() || (known(was[i].Name) && data[i].Name != was[i].Name.ValueString())
	}
	if changed {
		return fmt.Errorf("the instance's disks changed since the plan (%d now, %d in state); nothing was changed. Refresh and "+
			"plan again", len(data), len(was))
	}
	return nil
}

func spec(v client.Volume) client.VolumeSpec {
	return client.VolumeSpec{ID: v.ID, Root: v.Root, Name: v.Name, Size: v.Size, DatastoreID: v.DatastoreID}
}
