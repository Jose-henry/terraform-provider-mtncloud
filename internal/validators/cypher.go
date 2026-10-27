package validators

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
)

// CypherMounts are the Cypher mounts a key can start with.
func CypherMounts() []string { return []string{"secret", "tfvars", "password", "uuid", "key", "vault"} }

// CypherKey checks that a Cypher key starts with a mount and has a name after it, for example
// "secret/db-password".
func CypherKey() validator.String { return cypherKeyValidator{} }

type cypherKeyValidator struct{}

func (cypherKeyValidator) Description(context.Context) string {
	return "must start with a Cypher mount (" + strings.Join(CypherMounts(), "/, ") + "/) followed by a name"
}

func (v cypherKeyValidator) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}

func (v cypherKeyValidator) ValidateString(ctx context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	key := req.ConfigValue.ValueString()
	mount, name, ok := strings.Cut(key, "/")
	if ok && name != "" && !strings.HasSuffix(name, "/") && slices.Contains(CypherMounts(), mount) {
		return
	}
	resp.Diagnostics.AddAttributeError(req.Path, "Invalid Cypher key",
		fmt.Sprintf("%q %s, for example secret/db-password.", key, v.Description(ctx)))
}
