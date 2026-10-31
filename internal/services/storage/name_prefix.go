package storage

import (
	"context"
	"crypto/rand"
	"fmt"
	"strings"

	"github.com/mtn/terraform-provider-mtncloud/internal/planmodifiers"

	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
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

// prefixSetAtCreate refuses a new name_prefix the bucket's name doesn't start with: MTN Object
// Storage can't rename a bucket, and nothing is replaced to fake it (owner, 2026-10-31). A prefix the
// name already starts with (an imported bucket, or a name moved to name_prefix) is no change.
type prefixSetAtCreate struct{}

func (prefixSetAtCreate) Description(context.Context) string {
	return "Set when the bucket is created; a prefix its name doesn't start with is refused."
}

func (m prefixSetAtCreate) MarkdownDescription(ctx context.Context) string { return m.Description(ctx) }

func (prefixSetAtCreate) PlanModifyString(ctx context.Context, req planmodifier.StringRequest, resp *planmodifier.StringResponse) {
	if req.State.Raw.IsNull() || req.PlanValue.Equal(req.StateValue) || req.PlanValue.IsUnknown() || req.PlanValue.IsNull() {
		return
	}
	var name string
	if d := req.State.GetAttribute(ctx, rootName, &name); d.HasError() {
		return
	}
	if !strings.HasPrefix(name, req.PlanValue.ValueString()) {
		planmodifiers.RefuseChange(&resp.Diagnostics, req.Path, "bucket", req.StateValue)
	}
}
