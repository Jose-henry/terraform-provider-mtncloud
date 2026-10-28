// Package automation holds the task resources built from one table (task_specs.go): each task type
// is a spec of its options, and one engine (task.go) gives every type its schema, CRUD and import.
package automation

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/resource"

	"github.com/mtn/terraform-provider-mtncloud/internal/client"
)

// Resources lists every resource in this package, for the provider to register.
func Resources() []func() resource.Resource {
	out := make([]func() resource.Resource, 0, len(taskSpecs()))
	for _, s := range taskSpecs() {
		out = append(out, func() resource.Resource { return &taskResource{spec: s} })
	}
	return out
}

// base gives every resource its client.
type base struct {
	client *client.Client
}

func (b *base) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return // validate or an unconfigured provider; CRUD checks ready()
	}
	c, ok := req.ProviderData.(*client.Client)
	if !ok {
		resp.Diagnostics.AddError("Unexpected provider data", fmt.Sprintf("expected *client.Client, got %T. This is a provider bug.", req.ProviderData))
		return
	}
	b.client = c
}

// ready reports whether the provider is configured; see lookup.base.ready for when it isn't.
func (b *base) ready(diags *diag.Diagnostics) bool {
	if b.client == nil {
		diags.AddError("MTN Cloud provider is not configured yet",
			"The provider block uses values that are only known after apply. Use values known at plan time, or apply the resources they come from first.")
		return false
	}
	return true
}

func apiError(diags *diag.Diagnostics, what string, err error) {
	diags.AddError("MTN Cloud API error", fmt.Sprintf("Unable to %s: %s", what, err))
}
