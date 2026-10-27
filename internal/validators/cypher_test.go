package validators

import (
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

func TestCypherKey(t *testing.T) {
	for key, ok := range map[string]bool{
		"secret/db-password": true, "password/20/app-admin": true, "tfvars/prod": true, "uuid/x": true,
		"key/256/aes": true, "vault/kv/app/data/token": true,
		"db-password": false, "secret/": false, "secret/a/": false, "secrets/x": false, "/x": false, "": false,
	} {
		resp := &validator.StringResponse{}
		CypherKey().ValidateString(t.Context(), validator.StringRequest{Path: path.Root("key"), ConfigValue: types.StringValue(key)}, resp)
		if resp.Diagnostics.HasError() == ok {
			t.Errorf("%q: error %v, want valid %v", key, resp.Diagnostics.HasError(), ok)
		}
	}
}
