package storage

import (
	"context"
	"crypto/rand"
	"fmt"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
)

// suffixLen is how many random characters name_prefix gets. 36^8 names per prefix: a clash with
// another tenant is unlikely, and Create picks again when one happens.
const suffixLen = 8

const suffixChars = "abcdefghijklmnopqrstuvwxyz0123456789"

// prefixedName is prefix plus suffixLen random lower-case letters and digits.
func prefixedName(prefix string) string {
	b := make([]byte, suffixLen)
	_, _ = rand.Read(b) // never fails (crypto/rand)
	for i := range b {
		b[i] = suffixChars[int(b[i])%len(suffixChars)]
	}
	return prefix + string(b)
}

// prefixValidator refuses a prefix that can't start a valid bucket name.
type prefixValidator struct{}

func (prefixValidator) Description(context.Context) string {
	return "the start of a bucket name: lower-case letters, digits, dots and hyphens"
}
func (v prefixValidator) MarkdownDescription(ctx context.Context) string { return v.Description(ctx) }
func (prefixValidator) ValidateString(_ context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	prefix := req.ConfigValue.ValueString()
	if p := bucketNameProblem(prefix + strings.Repeat("a", suffixLen)); p != "" {
		resp.Diagnostics.AddAttributeError(req.Path, "Invalid name prefix",
			fmt.Sprintf("%q can't start a bucket name: %s.", prefix, p))
	}
}

// prefixChanged replaces the bucket when name_prefix changes, unless the bucket's name already
// starts with the new prefix (an imported bucket, or a name moved to name_prefix).
func prefixChanged(ctx context.Context, req planmodifier.StringRequest, resp *stringplanmodifier.RequiresReplaceIfFuncResponse) {
	if req.State.Raw.IsNull() || req.PlanValue.Equal(req.StateValue) || req.PlanValue.IsUnknown() {
		return
	}
	var name string
	if d := req.State.GetAttribute(ctx, rootName, &name); d.HasError() {
		return
	}
	resp.RequiresReplace = !req.PlanValue.IsNull() && !strings.HasPrefix(name, req.PlanValue.ValueString())
}
