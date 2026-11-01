package validators

import (
	"context"
	"fmt"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
)

// MixedPassword checks a password has at least min characters, with a lowercase letter, an
// uppercase letter and a digit: the rule MTN's catalog forms state as
// `^(?=.*[a-z])(?=.*[A-Z])(?=.*\d).{8,}$` (RE2 has no lookahead, so it is checked here). The value
// is never echoed in the error.
func MixedPassword(min int) validator.String { return passwordValidator{min: min} }

type passwordValidator struct{ min int }

func (v passwordValidator) Description(context.Context) string {
	return fmt.Sprintf("must be at least %d characters, with a lowercase letter, an uppercase letter and a digit", v.min)
}

func (v passwordValidator) MarkdownDescription(ctx context.Context) string { return v.Description(ctx) }

func (v passwordValidator) ValidateString(ctx context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	s := req.ConfigValue.ValueString()
	var missing []string
	if len([]rune(s)) < v.min {
		missing = append(missing, fmt.Sprintf("at least %d characters", v.min))
	}
	for _, c := range []struct {
		what string
		is   func(rune) bool
	}{{"a lowercase letter", in('a', 'z')}, {"an uppercase letter", in('A', 'Z')}, {"a digit", in('0', '9')}} {
		if !strings.ContainsFunc(s, c.is) {
			missing = append(missing, c.what)
		}
	}
	if len(missing) > 0 {
		resp.Diagnostics.AddAttributeError(req.Path, "Password too weak",
			"MTN Cloud refuses this password: it needs "+strings.Join(missing, ", ")+".")
	}
}

// in matches an ASCII range, as the form's [a-z], [A-Z] and \d do.
func in(lo, hi rune) func(rune) bool { return func(r rune) bool { return r >= lo && r <= hi } }
