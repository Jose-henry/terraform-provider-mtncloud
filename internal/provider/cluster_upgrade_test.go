package provider

import (
	"encoding/json"
	"testing"

	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

// A 0.11.0 cluster state (schema version 0) keeps its `root_datastore` as `disk_datastore`, so a
// configuration that renames the argument plans no new cluster.
func TestCluster_UpgradeKeepsDiskDatastore(t *testing.T) {
	for _, tc := range []struct{ typ, block string }{
		{"mtncloud_docker_cluster", "host"},
		{"mtncloud_managed_kubernetes_cluster", "master"},
	} {
		t.Run(tc.typ, func(t *testing.T) {
			ctx := t.Context()
			newServer, err := Server(ctx, "test")
			if err != nil {
				t.Fatal(err)
			}
			s := newServer()
			sch, err := s.GetProviderSchema(ctx, &tfprotov6.GetProviderSchemaRequest{})
			if err != nil {
				t.Fatal(err)
			}
			rs := sch.ResourceSchemas[tc.typ]
			if rs == nil || rs.Version != 1 {
				t.Fatalf("%s: want schema version 1, got %+v", tc.typ, rs)
			}
			typ := rs.ValueType().(tftypes.Object)
			v0 := map[string]any{}
			for name := range typ.AttributeTypes {
				v0[name] = nil
			}
			v0["id"], v0["name"] = "5", "tf-acc-up"
			node := map[string]any{"plan": "G4S16", "root_datastore": "ENCR_AT_REST", "data_volume": []any{map[string]any{"size": 50, "datastore": nil}},
				"networks": []any{"net"}, "security_groups": nil}
			v0[tc.block] = node
			if tc.block == "master" {
				w := map[string]any{"plan": nil, "root_datastore": "SSD", "data_volume": nil, "networks": nil, "security_groups": nil}
				v0["worker"] = w
			}
			raw, _ := json.Marshal(v0)
			resp, err := s.UpgradeResourceState(ctx, &tfprotov6.UpgradeResourceStateRequest{
				TypeName: tc.typ, Version: 0, RawState: &tfprotov6.RawState{JSON: raw}})
			if err != nil {
				t.Fatal(err)
			}
			for _, d := range resp.Diagnostics {
				t.Errorf("%s: %s", d.Summary, d.Detail)
			}
			if resp.UpgradedState == nil {
				t.Fatal("no upgraded state")
			}
			got, err := resp.UpgradedState.Unmarshal(typ)
			if err != nil {
				t.Fatal(err)
			}
			var top map[string]tftypes.Value
			if err := got.As(&top); err != nil {
				t.Fatal(err)
			}
			check := func(block, want string) {
				var n map[string]tftypes.Value
				if err := top[block].As(&n); err != nil {
					t.Fatal(err)
				}
				var ds string
				if err := n["disk_datastore"].As(&ds); err != nil || ds != want {
					t.Errorf("%s.disk_datastore = %q (%v), want %q", block, ds, err, want)
				}
			}
			check(tc.block, "ENCR_AT_REST")
			if tc.block == "master" {
				check("worker", "SSD")
			}
		})
	}
}
