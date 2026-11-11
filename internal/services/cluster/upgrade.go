package cluster

import (
	"context"
	"encoding/json"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
)

// schemaVersion 1: 0.11.1 renamed the node blocks' `root_datastore` to `disk_datastore`.
const schemaVersion = 1

var _ resource.ResourceWithUpgradeState = (*clusterResource)(nil)

// UpgradeState carries a 0.11.0 state over. Without it the stored `root_datastore` would be dropped,
// and a configuration that renamed it to `disk_datastore` would plan a new cluster.
func (r *clusterResource) UpgradeState(context.Context) map[int64]resource.StateUpgrader {
	return map[int64]resource.StateUpgrader{0: {StateUpgrader: upgradeV0}}
}

func upgradeV0(_ context.Context, req resource.UpgradeStateRequest, resp *resource.UpgradeStateResponse) {
	if req.RawState == nil {
		resp.Diagnostics.AddError("Can't upgrade the cluster's state", "The stored state is empty.")
		return
	}
	out, err := renameDiskDatastore(req.RawState.JSON)
	if err != nil {
		resp.Diagnostics.AddError("Can't upgrade the cluster's state", "Reading the stored state: "+err.Error())
		return
	}
	resp.DynamicValue = &tfprotov6.DynamicValue{JSON: out}
}

// renameDiskDatastore moves `root_datastore` to `disk_datastore` in the master, host and worker
// blocks of a version 0 state, which is otherwise the same shape as version 1.
func renameDiskDatastore(raw []byte) ([]byte, error) {
	var st map[string]any
	if err := json.Unmarshal(raw, &st); err != nil {
		return nil, err
	}
	for _, block := range []string{"master", "host", "worker"} {
		n, ok := st[block].(map[string]any)
		if !ok {
			continue
		}
		v, had := n["root_datastore"]
		delete(n, "root_datastore")
		if _, set := n["disk_datastore"]; !set {
			if !had {
				v = nil
			}
			n["disk_datastore"] = v
		}
	}
	return json.Marshal(st)
}
