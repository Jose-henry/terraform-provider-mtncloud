package loadbalancer

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
)

// clusterMemberSchemaVersion 1 (0.11.3): a Docker cluster's hosts are stored as docker-worker. 0.11.2
// stored the cloud's own code, which a customer never sees (owner, 2026-11-19).
const clusterMemberSchemaVersion = 1

var _ resource.ResourceWithUpgradeState = (*clusterMemberResource)(nil)

// UpgradeState renames the role in a 0.11.2 state before Terraform refreshes or plans, so the old
// code never shows, not as drift, nor in a -refresh=false plan.
func (r *clusterMemberResource) UpgradeState(context.Context) map[int64]resource.StateUpgrader {
	return map[int64]resource.StateUpgrader{0: {StateUpgrader: upgradeClusterMemberV0}}
}

func upgradeClusterMemberV0(_ context.Context, req resource.UpgradeStateRequest, resp *resource.UpgradeStateResponse) {
	if req.RawState == nil {
		resp.Diagnostics.AddError("Can't upgrade the cluster member's state", "The stored state is empty.")
		return
	}
	out, err := renameDockerRole(req.RawState.JSON)
	if err != nil {
		resp.Diagnostics.AddError("Can't upgrade the cluster member's state", "Reading the stored state: "+err.Error())
		return
	}
	resp.DynamicValue = &tfprotov6.DynamicValue{JSON: out}
}

// renameDockerRole writes the cloud's code for a Docker cluster's hosts as docker-worker in role and
// in the <cluster_id>:<role> ID of a version 0 state, which is otherwise the same shape as version 1.
func renameDockerRole(raw []byte) ([]byte, error) {
	var st map[string]any
	if err := json.Unmarshal(raw, &st); err != nil {
		return nil, err
	}
	if st["role"] == dockerHostRole {
		st["role"] = dockerWorkerRole
		if id, ok := st["id"].(string); ok {
			if c, _, found := strings.Cut(id, ":"); found {
				st["id"] = c + ":" + dockerWorkerRole
			}
		}
	}
	return json.Marshal(st)
}
