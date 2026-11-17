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
	for role, warn := range map[string]bool{"docker-host": true, dockerHostRole: false, "kube-worker": false} {
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
		if warn && got && !strings.Contains(resp.Diagnostics.Warnings()[0].Detail(), "kube-worker and kube-master") {
			t.Errorf("role %s: warning doesn't list the known roles: %v", role, resp.Diagnostics)
		}
	}
}
