package catalog

import (
	"context"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/resource"
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

// The old names were removed before the first production release (owner, 2026-11-01; D-040).
func TestRemovedOrderNames(t *testing.T) {
	for _, f := range orderResources() {
		switch n := f().(*orderResource).spec.typeName; n {
		case "managed_kubernetes", "vpn_service":
			t.Errorf("mtncloud_%s is still registered", n)
		}
	}
	for _, n := range []string{"self_managed_kubernetes", "pfsense_vpn"} {
		if s := orderSchema(t, orderType(t, n)); s.Schema.DeprecationMessage != "" {
			t.Errorf("mtncloud_%s is deprecated: %q", n, s.Schema.DeprecationMessage)
		}
	}
}
