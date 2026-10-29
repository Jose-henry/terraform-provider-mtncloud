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
