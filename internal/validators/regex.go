// Package validators holds plan-time checks shared by resources and data sources, so mistakes are
// caught by terraform plan, not by the API.
package validators

import (
	"context"
	"fmt"
	"regexp"

	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
)

// Regex checks that a string is a valid RE2 regular expression.
func Regex() validator.String { return regexValidator{} }

type regexValidator struct{}

func (regexValidator) Description(context.Context) string {
	return "must be a valid regular expression (RE2 syntax)"
}

func (v regexValidator) MarkdownDescription(ctx context.Context) string { return v.Description(ctx) }

func (regexValidator) ValidateString(_ context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	if _, err := regexp.Compile(req.ConfigValue.ValueString()); err != nil {
		resp.Diagnostics.AddAttributeError(req.Path, "Invalid regular expression",
			fmt.Sprintf("%q is not a valid regular expression: %s", req.ConfigValue.ValueString(), err))
	}
}
