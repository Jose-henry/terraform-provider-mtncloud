package legacy

import (
	"context"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-sdk/v2/diag"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
)

// A value that doesn't fit its attribute becomes a warning naming it, not a silent no-op or an error;
// values that fit are saved as before.
func TestSetAttrWarns(t *testing.T) {
	read := func(ctx context.Context, d *schema.ResourceData, _ any) diag.Diagnostics {
		setAttr(ctx, d, "count", "not a number")
		setAttr(ctx, d, "name", "web")
		return nil
	}
	r := &schema.Resource{ReadContext: read, Schema: map[string]*schema.Schema{
		"count": {Type: schema.TypeInt, Optional: true},
		"name":  {Type: schema.TypeString, Optional: true},
	}}
	withPermissionHints(&schema.Provider{ResourcesMap: map[string]*schema.Resource{"mtncloud_thing": r}})

	d := r.TestResourceData()
	diags := r.ReadContext(t.Context(), d, nil)
	if diags.HasError() {
		t.Fatalf("a bad value is an error: %v", diags)
	}
	if len(diags) != 1 || diags[0].Severity != diag.Warning || !strings.Contains(diags[0].Detail, "count") {
		t.Fatalf("diags %+v, want one warning naming count", diags)
	}
	if strings.Contains(diags[0].Detail, "not a number") {
		t.Errorf("the warning shows the value (it could be a secret): %s", diags[0].Detail)
	}
	if strings.Contains(diags[0].Detail, "name") || d.Get("name") != "web" {
		t.Errorf("name wasn't saved: %q, %+v", d.Get("name"), diags)
	}

	// A number the API sends as a string is saved; an empty one keeps the previous value.
	r3 := &schema.Resource{ReadContext: func(ctx context.Context, d *schema.ResourceData, _ any) diag.Diagnostics {
		setIntFromString(ctx, d, "a", "42")
		setIntFromString(ctx, d, "b", "")
		return nil
	}, Schema: map[string]*schema.Schema{
		"a": {Type: schema.TypeInt, Optional: true},
		"b": {Type: schema.TypeInt, Optional: true},
	}}
	withPermissionHints(&schema.Provider{ResourcesMap: map[string]*schema.Resource{"mtncloud_ints": r3}})
	d3 := r3.TestResourceData()
	_ = d3.Set("b", 7)
	if diags := r3.ReadContext(t.Context(), d3, nil); len(diags) != 0 || d3.Get("a") != 42 || d3.Get("b") != 7 {
		t.Errorf("ints from strings: a=%v b=%v %+v", d3.Get("a"), d3.Get("b"), diags)
	}

	// A clean read has no warning.
	r2 := &schema.Resource{ReadContext: func(ctx context.Context, d *schema.ResourceData, _ any) diag.Diagnostics {
		setAttr(ctx, d, "name", "web")
		return nil
	}, Schema: map[string]*schema.Schema{"name": {Type: schema.TypeString, Optional: true}}}
	withPermissionHints(&schema.Provider{ResourcesMap: map[string]*schema.Resource{"mtncloud_other": r2}})
	if diags := r2.ReadContext(t.Context(), r2.TestResourceData(), nil); len(diags) != 0 {
		t.Errorf("clean read: %+v", diags)
	}
}
