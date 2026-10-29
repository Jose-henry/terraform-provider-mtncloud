package storage

import (
	"context"
	"maps"

	"github.com/hashicorp/terraform-plugin-framework-timeouts/resource/timeouts"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

// The bucket setting and object ACL resources share their model with a data source, which has no
// timeouts block. These helpers read and write such a model through the resource's schema without
// the block (core), and carry the block's own value alongside it.

// withTimeouts is a resource schema: core plus the timeouts block.
func withTimeouts(ctx context.Context, core schema.Schema, opts timeouts.Opts) schema.Schema {
	full := core
	full.Attributes = maps.Clone(core.Attributes)
	full.Attributes["timeouts"] = timeouts.Attributes(ctx, opts)
	return full
}

// coreGet reads a plan or state value (with the timeouts block) into a model without it, and
// returns the block.
func coreGet(ctx context.Context, core schema.Schema, full tfsdk.State, m any) (timeouts.Value, tftypes.Value, diag.Diagnostics) {
	var to timeouts.Value
	diags := full.GetAttribute(ctx, path.Root("timeouts"), &to)
	var vals map[string]tftypes.Value
	toRaw := tftypes.NewValue(full.Schema.Type().TerraformType(ctx).(tftypes.Object).AttributeTypes["timeouts"], nil)
	coreRaw := tftypes.NewValue(core.Type().TerraformType(ctx), nil)
	if !full.Raw.IsNull() {
		if err := full.Raw.As(&vals); err != nil {
			diags.AddError("Unable to read the configuration", err.Error()+". This is a provider bug.")
			return to, toRaw, diags
		}
		vals = maps.Clone(vals) // As hands back the value's own map: never change it
		if v, ok := vals["timeouts"]; ok {
			toRaw = v
		}
		delete(vals, "timeouts")
		coreRaw = tftypes.NewValue(core.Type().TerraformType(ctx), vals)
	}
	diags.Append(tfsdk.State{Schema: core, Raw: coreRaw}.Get(ctx, m)...)
	return to, toRaw, diags
}

// coreSet writes a model without the timeouts block into dst, with the block's value toRaw.
func coreSet(ctx context.Context, core schema.Schema, dst *tfsdk.State, m any, toRaw tftypes.Value) diag.Diagnostics {
	tmp := tfsdk.State{Schema: core, Raw: tftypes.NewValue(core.Type().TerraformType(ctx), nil)}
	diags := tmp.Set(ctx, m)
	if diags.HasError() {
		return diags
	}
	var vals map[string]tftypes.Value
	if err := tmp.Raw.As(&vals); err != nil {
		diags.AddError("Unable to save the state", err.Error()+". This is a provider bug.")
		return diags
	}
	vals["timeouts"] = toRaw
	dst.Raw = tftypes.NewValue(dst.Schema.Type().TerraformType(ctx), vals)
	return diags
}
