package planmodifiers

import (
	"context"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

func TestSetAtCreate(t *testing.T) {
	exists := tfsdk.State{Raw: tftypes.NewValue(tftypes.Object{}, map[string]tftypes.Value{})}
	creating := tfsdk.State{Raw: tftypes.NewValue(tftypes.Object{}, nil)}
	for _, tc := range []struct {
		name     string
		state    tfsdk.State
		from, to types.String
		adopt    bool
		wantErr  string
	}{
		{"create", creating, types.StringNull(), types.StringValue("a"), false, ""},
		{"unchanged", exists, types.StringValue("a"), types.StringValue("a"), false, ""},
		{"known at apply", exists, types.StringValue("a"), types.StringUnknown(), false, ""},
		{"changed", exists, types.StringValue("a"), types.StringValue("b"), false, `Set it back to "a".`},
		{"removed", exists, types.StringValue("a"), types.StringNull(), false, `Set it back to "a".`},
		{"added", exists, types.StringNull(), types.StringValue("b"), false, "Remove it"},
		{"adopted after import", exists, types.StringNull(), types.StringValue("b"), true, ""},
		{"changed after import", exists, types.StringValue("a"), types.StringValue("b"), true, "Set it back"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := SetAtCreate("router")
			if tc.adopt {
				m = SetAtCreateUnlessImported("router")
			}
			resp := &planmodifier.StringResponse{PlanValue: tc.to}
			m.PlanModifyString(context.Background(), planmodifier.StringRequest{Path: path.Root("name"), State: tc.state,
				StateValue: tc.from, PlanValue: tc.to}, resp)
			got := ""
			if resp.Diagnostics.HasError() {
				got = resp.Diagnostics.Errors()[0].Detail()
			}
			if tc.wantErr == "" && got != "" || tc.wantErr != "" && !strings.Contains(got, tc.wantErr) {
				t.Errorf("error %q, want one containing %q", got, tc.wantErr)
			}
			if !resp.PlanValue.Equal(tc.to) {
				t.Errorf("plan changed to %v", resp.PlanValue)
			}
		})
	}
}

// A sensitive attribute's value never appears in the refusal.
func TestSetAtCreateSensitive(t *testing.T) {
	exists := tfsdk.State{Raw: tftypes.NewValue(tftypes.Object{}, map[string]tftypes.Value{})}
	resp := &planmodifier.StringResponse{}
	SetAtCreateSensitive("instance").PlanModifyString(context.Background(), planmodifier.StringRequest{Path: path.Root("env_vars"),
		State: exists, StateValue: types.StringValue("s3cret"), PlanValue: types.StringValue("other")}, resp)
	if !resp.Diagnostics.HasError() {
		t.Fatal("no error for a change")
	}
	if d := resp.Diagnostics.Errors()[0].Detail(); strings.Contains(d, "s3cret") || !strings.Contains(d, "the value the instance was made with") {
		t.Errorf("detail %q shows the value, or doesn't say what to do", d)
	}
}
