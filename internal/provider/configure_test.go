package provider

import (
	"testing"

	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

// secure = false warns once per configure: the muxed provider configures both halves, and only one
// of them may say it.
func TestConfigure_InsecureWarnsOnce(t *testing.T) {
	ctx := t.Context()
	newServer, err := Server(ctx, "test")
	if err != nil {
		t.Fatal(err)
	}
	s := newServer()
	schema, err := s.GetProviderSchema(ctx, &tfprotov6.GetProviderSchemaRequest{})
	if err != nil {
		t.Fatal(err)
	}
	typ := schema.Provider.ValueType().(tftypes.Object)
	vals := map[string]tftypes.Value{}
	for name, at := range typ.AttributeTypes {
		vals[name] = tftypes.NewValue(at, nil)
	}
	vals["url"] = tftypes.NewValue(tftypes.String, "https://console.example.test")
	vals["access_token"] = tftypes.NewValue(tftypes.String, "test-token")
	vals["secure"] = tftypes.NewValue(tftypes.Bool, false)
	cfg, err := tfprotov6.NewDynamicValue(typ, tftypes.NewValue(typ, vals))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := s.ConfigureProvider(ctx, &tfprotov6.ConfigureProviderRequest{Config: &cfg})
	if err != nil {
		t.Fatal(err)
	}
	warnings := 0
	for _, d := range resp.Diagnostics {
		if d.Severity == tfprotov6.DiagnosticSeverityError {
			t.Errorf("error: %s: %s", d.Summary, d.Detail)
		}
		if d.Severity == tfprotov6.DiagnosticSeverityWarning {
			warnings++
		}
	}
	if warnings != 1 {
		t.Errorf("%d warnings, want 1", warnings)
	}
}
