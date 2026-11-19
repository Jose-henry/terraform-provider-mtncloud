package loadbalancer

import (
	"context"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

// A role the member picker doesn't offer is warned about at plan, not found out minutes into the apply;
// a Docker cluster's hosts' role is a known one (tester's fix 20).
func TestClusterMemberRoleWarning(t *testing.T) {
	ctx := context.Background()
	r := &clusterMemberResource{}
	var sr resource.SchemaResponse
	r.Schema(ctx, resource.SchemaRequest{}, &sr)
	typ := sr.Schema.Type().TerraformType(ctx).(tftypes.Object)
	for role, warn := range map[string]bool{"docker-host": true, dockerWorkerRole: false, "kube-worker": false} {
		vals := map[string]tftypes.Value{}
		for name, at := range typ.AttributeTypes {
			vals[name] = tftypes.NewValue(at, nil)
		}
		vals["role"] = tftypes.NewValue(tftypes.String, role)
		cfg := tfsdk.Config{Schema: sr.Schema, Raw: tftypes.NewValue(typ, vals)}
		var resp resource.ValidateConfigResponse
		r.ValidateConfig(ctx, resource.ValidateConfigRequest{Config: cfg}, &resp)
		got := resp.Diagnostics.WarningsCount() > 0
		if got != warn || resp.Diagnostics.HasError() {
			t.Errorf("role %s: warning %v (%v), want %v", role, got, resp.Diagnostics, warn)
		}
		if warn && got && !strings.Contains(resp.Diagnostics.Warnings()[0].Detail(), "kube-worker and kube-master") ||
			warn && got && !strings.Contains(resp.Diagnostics.Warnings()[0].Detail(), "docker-worker") {
			t.Errorf("role %s: warning doesn't list the known roles: %v", role, resp.Diagnostics)
		}
	}
}

// docker-worker goes on the wire as the cloud's own code; other roles go as written.
func TestClusterMemberWireRole(t *testing.T) {
	for in, want := range map[string]string{dockerWorkerRole: dockerHostRole, "kube-worker": "kube-worker", "kube-master": "kube-master"} {
		if got := wireRole(in); got != want {
			t.Errorf("wireRole(%q) = %q, want %q", in, got, want)
		}
	}
}

// The cloud's own code for docker-worker is never shown (owner, 2026-11-19): written in a
// configuration it is a plan-time error that names only docker-worker.
func TestClusterMemberRefusesWireRole(t *testing.T) {
	ctx := context.Background()
	r := &clusterMemberResource{}
	var sr resource.SchemaResponse
	r.Schema(ctx, resource.SchemaRequest{}, &sr)
	typ := sr.Schema.Type().TerraformType(ctx).(tftypes.Object)
	vals := map[string]tftypes.Value{}
	for name, at := range typ.AttributeTypes {
		vals[name] = tftypes.NewValue(at, nil)
	}
	vals["role"] = tftypes.NewValue(tftypes.String, dockerHostRole)
	var resp resource.ValidateConfigResponse
	r.ValidateConfig(ctx, resource.ValidateConfigRequest{Config: tfsdk.Config{Schema: sr.Schema, Raw: tftypes.NewValue(typ, vals)}}, &resp)
	if !resp.Diagnostics.HasError() {
		t.Fatalf("the cloud's code is not refused: %v", resp.Diagnostics)
	}
	for _, d := range resp.Diagnostics {
		if strings.Contains(d.Summary()+d.Detail(), dockerHostRole) || !strings.Contains(d.Detail(), dockerWorkerRole) {
			t.Errorf("diagnostic shows the cloud's code or not docker-worker: %s: %s", d.Summary(), d.Detail())
		}
	}
}

// A 0.11.2 state stored the cloud's code in role and id: version 1 stores docker-worker, before
// Terraform refreshes or plans. Other roles, and states without one, are kept as they are.
func TestClusterMemberUpgradeV0(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{`{"id":"144:` + dockerHostRole + `","role":"` + dockerHostRole + `","port":8080}`, `{"id":"144:docker-worker","port":8080,"role":"docker-worker"}`},
		{`{"id":"143:kube-worker","role":"kube-worker"}`, `{"id":"143:kube-worker","role":"kube-worker"}`},
		{`{"id":null,"role":null}`, `{"id":null,"role":null}`},
	} {
		got, err := renameDockerRole([]byte(c.in))
		if err != nil || string(got) != c.want {
			t.Errorf("renameDockerRole(%s) = %s, %v; want %s", c.in, got, err, c.want)
		}
	}
	r := &clusterMemberResource{}
	var sr resource.SchemaResponse
	r.Schema(context.Background(), resource.SchemaRequest{}, &sr)
	if sr.Schema.Version != 1 || r.UpgradeState(context.Background())[0].StateUpgrader == nil {
		t.Errorf("schema version %d, or no upgrader from version 0", sr.Schema.Version)
	}
}
