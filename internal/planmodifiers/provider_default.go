package planmodifiers

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// FillDefault sets attr in the plan to the provider-wide value (the provider block's attribute
// of the same name, or env) when the configuration leaves it out. With neither set, it fails,
// naming the three places to set it. Call it first in a resource's ModifyPlan: an attribute plan
// modifier can't do this, as the framework builds schemas on unconfigured resource instances.
func FillDefault(ctx context.Context, config tfsdk.Config, plan *tfsdk.Plan, attr, env, value string) diag.Diagnostics {
	var diags diag.Diagnostics
	var v types.String
	diags.Append(config.GetAttribute(ctx, path.Root(attr), &v)...)
	if diags.HasError() || !v.IsNull() {
		return diags
	}
	if value == "" {
		diags.AddAttributeError(path.Root(attr), "No "+attr+" set",
			fmt.Sprintf("Set %s on this resource, or %s in the provider block, or the %s environment variable.", attr, attr, env))
		return diags
	}
	diags.Append(plan.SetAttribute(ctx, path.Root(attr), types.StringValue(value))...)
	return diags
}
