package catalog

import (
	"context"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

// orderType builds the order resource registered under a type name.
func orderType(t *testing.T, name string) *orderResource {
	t.Helper()
	for _, f := range orderResources() {
		r := f().(*orderResource)
		if r.spec.typeName == name {
			return r
		}
	}
	t.Fatalf("no order resource %q", name)
	return nil
}

func orderSchema(t *testing.T, r *orderResource) resource.SchemaResponse {
	t.Helper()
	var s resource.SchemaResponse
	r.Schema(context.Background(), resource.SchemaRequest{}, &s)
	if s.Diagnostics.HasError() {
		t.Fatal(s.Diagnostics)
	}
	return s
}

func TestSelfManagedKubernetes_Alias(t *testing.T) {
	cur, old := orderType(t, "self_managed_kubernetes"), orderType(t, "managed_kubernetes")
	if !cur.spec.app || !old.spec.app {
		t.Fatal("both names must delete through the app")
	}
	if s := orderSchema(t, cur); s.Schema.DeprecationMessage != "" {
		t.Errorf("new name is deprecated: %q", s.Schema.DeprecationMessage)
	}
	if s := orderSchema(t, old); s.Schema.DeprecationMessage == "" {
		t.Error("old name has no deprecation message")
	}
	if len(old.MoveState(context.Background())) != 0 {
		t.Error("the alias must not accept moves")
	}
}

func TestSelfManagedKubernetes_MoveState(t *testing.T) {
	ctx := context.Background()
	r := orderType(t, "self_managed_kubernetes")
	s := orderSchema(t, r).Schema
	ty := s.Type().TerraformType(ctx)
	vals := map[string]tftypes.Value{}
	for k, at := range ty.(tftypes.Object).AttributeTypes {
		vals[k] = tftypes.NewValue(at, nil)
	}
	vals["id"] = tftypes.NewValue(tftypes.String, "9801")
	vals["name"] = tftypes.NewValue(tftypes.String, "apps-k8s")
	src := tfsdk.State{Schema: s, Raw: tftypes.NewValue(ty, vals)}

	movers := r.MoveState(ctx)
	if len(movers) != 1 {
		t.Fatalf("got %d movers", len(movers))
	}
	for _, c := range []struct {
		name, typ, addr string
		moved           bool
	}{
		{"old name", "mtncloud_managed_kubernetes", "registry.terraform.io/jose-henry/mtncloud", true},
		{"other type", "mtncloud_mysql", "registry.terraform.io/jose-henry/mtncloud", false},
		{"other provider", "mtncloud_managed_kubernetes", "registry.terraform.io/acme/other", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			resp := resource.MoveStateResponse{TargetState: tfsdk.State{Schema: s, Raw: tftypes.NewValue(ty, nil)}}
			movers[0].StateMover(ctx, resource.MoveStateRequest{SourceTypeName: c.typ, SourceProviderAddress: c.addr, SourceState: &src}, &resp)
			if resp.Diagnostics.HasError() {
				t.Fatal(resp.Diagnostics)
			}
			if got := !resp.TargetState.Raw.IsNull(); got != c.moved {
				t.Fatalf("moved = %v, want %v", got, c.moved)
			}
			if !c.moved {
				return
			}
			var id string
			resp.Diagnostics.Append(resp.TargetState.GetAttribute(ctx, path.Root("id"), &id)...)
			if id != "9801" {
				t.Errorf("id = %q", id)
			}
		})
	}
}

// PfSense's rename also dropped its disks (the form fixes them): what both schemas have moves, the
// disks stay behind.
func TestPfSense_MoveState(t *testing.T) {
	ctx := context.Background()
	cur, old := orderType(t, "pfsense_vpn"), orderType(t, "vpn_service")
	src, dst := orderSchema(t, old).Schema, orderSchema(t, cur).Schema
	if _, ok := src.Attributes["root_volume_size"]; !ok {
		t.Fatal("the old name lost its arguments: existing configurations would break")
	}
	if _, ok := dst.Attributes["root_volume_size"]; ok {
		t.Fatal("PfSense's disks are fixed by the form: no root_volume_size")
	}
	sty, dty := src.Type().TerraformType(ctx), dst.Type().TerraformType(ctx)
	vals := map[string]tftypes.Value{}
	for k, at := range sty.(tftypes.Object).AttributeTypes {
		vals[k] = tftypes.NewValue(at, nil)
	}
	vals["id"] = tftypes.NewValue(tftypes.String, "2726")
	vals["name"] = tftypes.NewValue(tftypes.String, "vpn-1")
	vals["root_volume_size"] = tftypes.NewValue(tftypes.Number, 50)
	vals["networks"] = tftypes.NewValue(tftypes.List{ElementType: tftypes.String}, []tftypes.Value{
		tftypes.NewValue(tftypes.String, "inside"), tftypes.NewValue(tftypes.String, "outside")})
	state := tfsdk.State{Schema: src, Raw: tftypes.NewValue(sty, vals)}
	movers := cur.MoveState(ctx)
	if len(movers) != 1 {
		t.Fatalf("got %d movers", len(movers))
	}
	resp := resource.MoveStateResponse{TargetState: tfsdk.State{Schema: dst, Raw: tftypes.NewValue(dty, nil)}}
	movers[0].StateMover(ctx, resource.MoveStateRequest{SourceTypeName: "mtncloud_vpn_service",
		SourceProviderAddress: "registry.terraform.io/jose-henry/mtncloud", SourceState: &state}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatal(resp.Diagnostics)
	}
	if !resp.TargetState.Raw.Type().Equal(dty) {
		t.Fatalf("moved state has the old schema's shape: %s", resp.TargetState.Raw.Type())
	}
	var id, name string
	var nets []string
	resp.Diagnostics.Append(resp.TargetState.GetAttribute(ctx, path.Root("id"), &id)...)
	resp.Diagnostics.Append(resp.TargetState.GetAttribute(ctx, path.Root("name"), &name)...)
	resp.Diagnostics.Append(resp.TargetState.GetAttribute(ctx, path.Root("networks"), &nets)...)
	if id != "2726" || name != "vpn-1" || len(nets) != 2 {
		t.Errorf("moved id=%q name=%q networks=%v", id, name, nets)
	}
}
