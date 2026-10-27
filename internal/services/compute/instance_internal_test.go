package compute

import (
	"context"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/mtn/terraform-provider-mtncloud/internal/client"
)

func strList(vals ...string) types.List {
	elems := make([]attr.Value, len(vals))
	for i, v := range vals {
		elems[i] = types.StringValue(v)
	}
	return types.ListValueMust(types.StringType, elems)
}

func TestAppendOnly(t *testing.T) {
	for _, tc := range []struct {
		name        string
		plan, state types.List
		want        bool
	}{
		{"same", strList("1", "2"), strList("1", "2"), true},
		{"appended", strList("1", "2", "3"), strList("1", "2"), true},
		{"first changed", strList("3", "2"), strList("1", "2"), false},
		{"removed", strList("1"), strList("1", "2"), false},
		{"reordered", strList("2", "1"), strList("1", "2"), false},
		{"unknown", types.ListUnknown(types.StringType), strList("1"), false},
		{"unknown element", types.ListValueMust(types.StringType, []attr.Value{types.StringUnknown()}), strList("1"), false},
		{"from empty state", strList("1"), types.ListNull(types.StringType), true},
	} {
		if got := appendOnly(tc.plan, tc.state); got != tc.want {
			t.Errorf("%s: appendOnly = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestSameName(t *testing.T) {
	for _, tc := range []struct {
		a, b types.String
		want bool
	}{
		{types.StringNull(), types.StringNull(), true},
		{types.StringValue("ENCR_AT_REST"), types.StringValue("encr_at_rest"), true},
		{types.StringValue("a"), types.StringNull(), false},
		{types.StringNull(), types.StringValue("a"), false},
		{types.StringValue("a"), types.StringValue("b"), false},
	} {
		if got := sameName(tc.a, tc.b); got != tc.want {
			t.Errorf("sameName(%s, %s) = %v, want %v", tc.a, tc.b, got, tc.want)
		}
	}
}

func TestSecurityGroupNames(t *testing.T) {
	ctx := context.Background()
	applied := []client.Option{{ID: 1, Name: "web-prod-Acme-default-sg"}, {ID: 2, Name: "Web"}}
	set := func(vals ...string) types.Set {
		s, _ := types.SetValueFrom(ctx, types.StringType, vals)
		return s
	}
	for _, tc := range []struct {
		name string
		was  types.Set
		want types.Set
	}{
		{"default hidden, case kept", set("web"), set("web")},
		{"default shown when listed", set("web", "web-prod-acme-default-sg"), set("web", "web-prod-acme-default-sg")},
		{"import shows the API names", types.SetNull(types.StringType), set("Web")},
	} {
		if got := securityGroupNames(ctx, applied, tc.was); !got.Equal(tc.want) {
			t.Errorf("%s: got %s, want %s", tc.name, got, tc.want)
		}
	}
	if got := securityGroupNames(ctx, applied[:1], types.SetNull(types.StringType)); !got.IsNull() {
		t.Errorf("only the default, nothing configured: got %s, want null", got)
	}
}

func TestOrNull(t *testing.T) {
	ctx := context.Background()
	if got := setOrNull(ctx, nil, types.SetNull(types.StringType)); !got.IsNull() {
		t.Errorf("empty labels, none configured: %s, want null", got)
	}
	empty, _ := types.SetValueFrom(ctx, types.StringType, []string{})
	if got := setOrNull(ctx, nil, empty); got.IsNull() || len(got.Elements()) != 0 {
		t.Errorf("empty labels, configured empty: %s, want []", got)
	}
	if got := mapOrNull(ctx, map[string]string{}, types.MapNull(types.StringType)); !got.IsNull() {
		t.Errorf("no tags, none configured: %s, want null", got)
	}
	if got := mapOrNull(ctx, map[string]string{"a": "1"}, types.MapNull(types.StringType)); len(got.Elements()) != 1 {
		t.Errorf("tags added out of band: %s, want them shown", got)
	}
}

func TestIsWindows(t *testing.T) {
	for name, want := range map[string]bool{
		"MTN Windows Server 2025 (BYOL)": true,
		"MTN-WINSVR2025":                 true,
		"MTN Ubuntu Server 24.04.3LTS":   false,
	} {
		if got := isWindows(client.InstanceType{Name: name}); got != want {
			t.Errorf("isWindows(%q) = %v, want %v", name, got, want)
		}
	}
}
