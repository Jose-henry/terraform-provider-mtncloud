package legacy

import (
	"context"
	"net/http"
	"sync"

	sdk "github.com/gomorpheus/morpheus-go-sdk"
	"github.com/hashicorp/terraform-plugin-sdk/v2/diag"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"

	"github.com/mtn/terraform-provider-mtncloud/internal/client"
)

type crudFunc = func(context.Context, *schema.ResourceData, interface{}) diag.Diagnostics

// withPermissionHints wraps every resource's and data source's create, read, update and delete so a
// 403 error says what to do about it. The SDK's error carries only the platform's message, not the
// status, so each call gets its own copy of the client and the wrapper reads that copy's last
// response. The copy also keeps concurrent calls from sharing the SDK's request counters. Importers
// get a copy too, so nothing writes to the shared client while a copy is taken.
func withPermissionHints(p *schema.Provider) {
	var (
		login sync.Mutex
		tried bool
	)
	// own returns a copy of the shared client, logged in once for every copy. A failed login isn't
	// tried again here: each copy then logs in itself and returns the error, as before.
	own := func(meta interface{}) (*sdk.Client, bool) {
		shared, ok := meta.(*sdk.Client)
		if !ok || shared == nil {
			return nil, false
		}
		login.Lock()
		defer login.Unlock()
		if !tried && !shared.IsLoggedIn() && shared.Username != "" {
			tried = true
			_, _ = shared.Login()
		}
		c := *shared
		return &c, true
	}
	wrap := func(fn crudFunc) crudFunc {
		if fn == nil {
			return nil
		}
		return func(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
			c, ok := own(meta)
			if !ok {
				return fn(ctx, d, meta)
			}
			diags := fn(ctx, d, c)
			return addForbiddenHint(diags, c.LastResponse())
		}
	}
	for _, r := range p.ResourcesMap {
		r.CreateContext = wrap(r.CreateContext)
		r.ReadContext = wrap(r.ReadContext)
		r.UpdateContext = wrap(r.UpdateContext)
		r.DeleteContext = wrap(r.DeleteContext)
		if r.Importer != nil && r.Importer.StateContext != nil {
			fn := r.Importer.StateContext
			r.Importer.StateContext = func(ctx context.Context, d *schema.ResourceData, meta interface{}) ([]*schema.ResourceData, error) {
				if c, ok := own(meta); ok {
					return fn(ctx, d, c)
				}
				return fn(ctx, d, meta)
			}
		}
	}
	for _, r := range p.DataSourcesMap {
		r.ReadContext = wrap(r.ReadContext)
	}
}

func addForbiddenHint(diags diag.Diagnostics, last *sdk.Response) diag.Diagnostics {
	if last == nil || last.StatusCode != http.StatusForbidden {
		return diags
	}
	// The client's own words, so both halves say the same: a role, the web firewall, or a network block.
	forbiddenHint := client.ForbiddenHint(last.Body)
	for i := range diags {
		if diags[i].Severity != diag.Error {
			continue
		}
		if diags[i].Detail == "" {
			diags[i].Detail = forbiddenHint
		} else {
			diags[i].Detail += "\n\n" + forbiddenHint
		}
	}
	return diags
}
