package legacy

import (
	"context"

	"github.com/hashicorp/terraform-plugin-sdk/v2/diag"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
)

// withKeptStateOnFailedUpdate makes a failed update keep the state it started from. By default the
// SDK saves the planned values even when the update fails, so the next refresh shows them "changed"
// back (tester's fix 18: a powershell task refused by MTN's firewall). Partial mode saves only what
// the update itself set, which on a failure is nothing.
func withKeptStateOnFailedUpdate(p *schema.Provider) {
	for _, r := range p.ResourcesMap {
		update := r.UpdateContext
		if update == nil {
			continue
		}
		r.UpdateContext = func(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
			diags := update(ctx, d, meta)
			if diags.HasError() {
				d.Partial(true)
			}
			return diags
		}
	}
}
