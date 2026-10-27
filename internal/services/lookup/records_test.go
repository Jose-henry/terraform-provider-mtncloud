package lookup

import (
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/mtn/terraform-provider-mtncloud/internal/client"
)

func TestConverters(t *testing.T) {
	if asText(3.5) != "3.5" || asText(true) != "true" || asText(nil) != "" ||
		asText(map[string]any{"name": "n"}) != "n" || asText(map[string]any{"id": 1.0, "username": "u"}) != "u" ||
		asText(map[string]any{"id": 1.0}) != "" {
		t.Error("asText")
	}
	if asNumber("42") != 42 || asNumber(7.0) != 7 || asNumber(map[string]any{"id": 9.0}) != 9 || asNumber(nil) != 0 || asNumber("x") != 0 {
		t.Error("asNumber")
	}
	if !asFlag("true") || !asFlag("on") || asFlag("off") || asFlag(nil) || !asFlag(true) {
		t.Error("asFlag")
	}
	if asList("x") != nil || len(asList([]any{1.0})) != 1 {
		t.Error("asList")
	}
}

func TestAt(t *testing.T) {
	r := client.Record{
		"zone":  map[string]any{"name": "z"},
		"roles": []any{map[string]any{"authority": "a"}, nil, map[string]any{"authority": "b"}},
	}
	if at(r, "zone.name") != "z" || at(r, "zone.missing") != nil || at(r, "missing.deep") != nil {
		t.Error("objects")
	}
	got := asList(at(r, "roles.authority"))
	if len(got) != 3 || got[0] != "a" || got[1] != nil || got[2] != "b" {
		t.Errorf("through a list with a nil element: %v", got)
	}
}

// Missing and null fields are known zero values and empty lists, never null: references to them
// must always work.
func TestFieldValueMissing(t *testing.T) {
	r := client.Record{"labels": nil}
	for _, c := range []struct {
		f    field
		want attr.Value
	}{
		{field{src: "description", kind: text}, types.StringValue("")},
		{field{src: "count", kind: number}, types.Int64Value(0)},
		{field{src: "on", kind: flag}, types.BoolValue(false)},
		{field{src: "labels", kind: texts}, types.ListValueMust(types.StringType, []attr.Value{})},
		{field{src: "ids.id", kind: numbers}, types.ListValueMust(types.Int64Type, []attr.Value{})},
	} {
		got := c.f.value(r)
		if got.IsNull() || !got.Equal(c.want) {
			t.Errorf("%s: got %v, want %v", c.f.src, got, c.want)
		}
	}
	// Empty strings and null elements are dropped from lists.
	r = client.Record{"labels": []any{"a", "", nil}, "ids": []any{map[string]any{"id": 1.0}, nil}}
	if v := (field{src: "labels", kind: texts}).value(r); !v.Equal(types.ListValueMust(types.StringType, []attr.Value{types.StringValue("a")})) {
		t.Errorf("texts: %v", v)
	}
	if v := (field{src: "ids.id", kind: numbers}).value(r); !v.Equal(types.ListValueMust(types.Int64Type, []attr.Value{types.Int64Value(1)})) {
		t.Errorf("numbers: %v", v)
	}
}

// No spec reads a field that holds a secret. Flags (passwordExpired, hasPrivateKey) only say
// whether one exists.
func TestSpecsReadNoSecrets(t *testing.T) {
	for _, s := range recordSpecs {
		for _, f := range s.fields {
			if f.kind == flag {
				continue
			}
			for _, bad := range []string{"password", "secret", "hash", "privatekey", "authkey", "token", "config"} {
				if strings.Contains(strings.ToLower(f.src), bad) {
					t.Errorf("%s.%s reads %q", s.key, f.attr, f.src)
				}
			}
		}
	}
}
