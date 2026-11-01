package loadbalancer

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/mtn/terraform-provider-mtncloud/internal/client"
)

// A group is the members the console added together and keeps in step: an instance's nodes
// (mtncloud_load_balancer_instance_member) or one role of a cluster's nodes
// (mtncloud_load_balancer_cluster_member). Both read and change their members the same way.

var groupMemberType = types.ObjectType{AttrTypes: map[string]attr.Type{"id": types.StringType, "address": types.StringType}}

// readGroup is a group's members, sorted by address, with the port, weight and backup it was added
// with: its first member shows them. Members that disagree (an update stopped part-way, or a change
// in the console) read as values no configuration has (weight -1, backup null), so the next plan
// sets every member again. ms must not be empty.
func readGroup(ms []client.LBMember) (members types.List, port, weight types.Int64, backup types.Bool) {
	flat := flattenMembers(ms) // sorted by address
	elems := make([]attr.Value, 0, len(flat))
	for _, x := range flat {
		elems = append(elems, types.ObjectValueMust(groupMemberType.AttrTypes, map[string]attr.Value{
			"id": types.StringValue(x.ID), "address": types.StringValue(x.Address)}))
	}
	port, weight, backup = types.Int64Value(flat[0].Port), types.Int64Value(flat[0].Weight), types.BoolValue(flat[0].Backup)
	for _, x := range flat[1:] {
		if x.Weight != flat[0].Weight {
			weight = types.Int64Value(-1)
		}
		if x.Backup != flat[0].Backup {
			backup = types.BoolNull()
		}
	}
	return types.ListValueMust(groupMemberType, elems), port, weight, backup
}

// setGroupWeight changes weight and backup on each member of a group in place: the members stay in
// the pool throughout. On a failure the members changed so far keep the new value; the error names
// the member that failed.
func (b *rbase) setGroupWeight(ctx context.Context, networkID int64, lbID, poolID string, members types.List, weight int64, backup bool) error {
	var ms []struct {
		ID      types.String `tfsdk:"id"`
		Address types.String `tfsdk:"address"`
	}
	if d := members.ElementsAs(ctx, &ms, false); d.HasError() {
		return fmt.Errorf("the members in state can't be read: %v", d)
	}
	for _, m := range ms {
		err := b.client.UpdateLBMember(ctx, networkID, lbID, poolID, m.ID.ValueString(), client.LBMemberSpec{Weight: &weight, Backup: &backup})
		if err != nil {
			return fmt.Errorf("member %s (%s): %w. The members changed so far keep the new value; the next apply sets "+
				"them all", m.ID.ValueString(), m.Address.ValueString(), err)
		}
	}
	return nil
}
