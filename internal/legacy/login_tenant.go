package legacy

import (
	"fmt"

	sdk "github.com/gomorpheus/morpheus-go-sdk"
)

// loginTenantID is the ID of the tenant the provider is logged in to, from /api/whoami (the SDK's
// whoami result leaves the account out). Resources that need a tenant default to it.
func loginTenantID(client *sdk.Client) (int64, error) {
	var out struct {
		User struct {
			Account struct {
				ID int64 `json:"id"`
			} `json:"account"`
		} `json:"user"`
	}
	if _, err := client.Execute(&sdk.Request{Method: "GET", Path: sdk.WhoamiPath, Result: &out}); err != nil {
		return 0, fmt.Errorf("couldn't read your tenant: %w", err)
	}
	if out.User.Account.ID == 0 {
		return 0, fmt.Errorf("couldn't read your tenant: the login's whoami has no tenant")
	}
	return out.User.Account.ID, nil
}

// ownTenant is the tenant a user or identity source is made in: always the login's own. tenant_id
// (deprecated) may only repeat it: a tenant's login can't manage another tenant, and MTN refuses it with
// a 403 that doesn't say why (owner, 2026-11-17). The tenant is stored in tenant_id.
func ownTenant(d interface {
	Get(string) interface{}
	Set(string, interface{}) error
}, client *sdk.Client) (int64, error) {
	own, err := loginTenantID(client)
	if err != nil {
		return 0, err
	}
	if set := d.Get("tenant_id").(int); set != 0 && int64(set) != own {
		return 0, fmt.Errorf("tenant_id %d isn't the tenant you are logged in to (%d): this provider only manages its own "+
			"tenant. Remove tenant_id", set, own)
	}
	return own, d.Set("tenant_id", int(own))
}

// tenantIDDeprecation is the deprecation note on tenant_id.
const tenantIDDeprecation = "tenant_id is always the tenant you are logged in to; remove it from the configuration. It will be removed in a later version."
