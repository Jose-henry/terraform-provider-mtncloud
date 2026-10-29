package storage

import (
	"context"
	"fmt"
	"maps"

	"github.com/hashicorp/terraform-plugin-framework-timeouts/resource/timeouts"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/mtn/terraform-provider-mtncloud/internal/client"
)

// settingSpec describes one bucket setting (ACL, CORS, lifecycle, logging, object lock). They all
// live and die the same way: the ID is the bucket's name, create and update write the setting and
// wait until every node returns it, destroy puts the bucket's default back, and a bucket deleted
// outside Terraform takes the setting with it.
type settingSpec[T any] struct {
	typeName   string // after "mtncloud_"
	about      string
	attributes map[string]schema.Attribute // besides id and bucket
	keys       func(*T) (id, bucket *types.String)
	read       func(ctx context.Context, c *client.Client, bucket string, m *T, diags *diag.Diagnostics)
	write      func(ctx context.Context, c *client.Client, bucket string, m *T) error
	clear      func(ctx context.Context, c *client.Client, bucket string) error
	validate   func(m *T, diags *diag.Diagnostics) // plan-time checks beyond the schema, optional
}

type setting[T any] struct {
	base
	spec settingSpec[T]
}

var (
	_ resource.ResourceWithImportState    = (*setting[aclModel])(nil)
	_ resource.ResourceWithValidateConfig = (*setting[aclModel])(nil)
)

func (r *setting[T]) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_" + r.spec.typeName
}

func (r *setting[T]) Schema(ctx context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = withTimeouts(ctx, r.core(), timeouts.Opts{Create: true, Update: true, Delete: true})
}

// core is the schema without the timeouts block: the shape of the model T, which the data source shares.
func (r *setting[T]) core() schema.Schema {
	attrs := map[string]schema.Attribute{
		"id": schema.StringAttribute{
			Description:   "The bucket's name.",
			Computed:      true,
			PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
		},
		"bucket": schema.StringAttribute{
			Description:   "The bucket's name in MOS, usually `mtncloud_mos_bucket.<name>.name`. Changing it moves the setting to another bucket.",
			Required:      true,
			Validators:    bucketNameValidators(),
			PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
		},
	}
	maps.Copy(attrs, r.spec.attributes)
	return schema.Schema{Description: r.spec.about, Attributes: attrs}
}

func (r *setting[T]) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	if r.spec.validate == nil {
		return
	}
	var m T
	_, _, d := coreGet(ctx, r.core(), tfsdk.State(req.Config), &m)
	resp.Diagnostics.Append(d...)
	if !resp.Diagnostics.HasError() {
		r.spec.validate(&m, &resp.Diagnostics)
	}
}

func (r *setting[T]) save(ctx context.Context, plan tfsdk.Plan, state *tfsdk.State, diags *diag.Diagnostics, op string) {
	if !r.ready(diags) {
		return
	}
	var m T
	to, toRaw, d := coreGet(ctx, r.core(), tfsdk.State(plan), &m)
	diags.Append(d...)
	if diags.HasError() {
		return
	}
	bound := to.Update
	if op == "set" {
		bound = to.Create
	}
	// Settling has no end of its own: bound it.
	timeout, d := bound(ctx, mosTimeout)
	diags.Append(d...)
	if diags.HasError() {
		return
	}
	id, bucket := r.spec.keys(&m)
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if err := r.spec.write(ctx, r.client, bucket.ValueString(), &m); err != nil {
		diags.AddError(fmt.Sprintf("Unable to %s the %s", op, what(r.spec.typeName)), err.Error())
		return
	}
	*id = *bucket
	diags.Append(coreSet(ctx, r.core(), state, &m, toRaw)...)
}

func (r *setting[T]) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	r.save(ctx, req.Plan, &resp.State, &resp.Diagnostics, "set")
}

func (r *setting[T]) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	r.save(ctx, req.Plan, &resp.State, &resp.Diagnostics, "change")
}

func (r *setting[T]) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	if !r.ready(&resp.Diagnostics) {
		return
	}
	var m T
	_, toRaw, d := coreGet(ctx, r.core(), req.State, &m)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}
	_, bucket := r.spec.keys(&m)
	var diags diag.Diagnostics
	r.spec.read(ctx, r.client, bucket.ValueString(), &m, &diags)
	if bucketGoneIn(diags) {
		resp.State.RemoveResource(ctx) // the bucket was deleted outside Terraform, and the setting with it
		return
	}
	resp.Diagnostics.Append(diags...)
	if !resp.Diagnostics.HasError() {
		resp.Diagnostics.Append(coreSet(ctx, r.core(), &resp.State, &m, toRaw)...)
	}
}

func (r *setting[T]) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	if !r.ready(&resp.Diagnostics) {
		return
	}
	var m T
	to, _, d := coreGet(ctx, r.core(), req.State, &m)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}
	timeout, d := to.Delete(ctx, mosTimeout)
	resp.Diagnostics.Append(d...)
	_, bucket := r.spec.keys(&m)
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if err := r.spec.clear(ctx, r.client, bucket.ValueString()); err != nil && !bucketGone(err) {
		resp.Diagnostics.AddError("Unable to remove the "+what(r.spec.typeName), err.Error())
	}
}

// ImportState takes the bucket's name.
func (r *setting[T]) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	if p := bucketNameProblem(req.ID); p != "" {
		resp.Diagnostics.AddError("Invalid import ID", fmt.Sprintf("Import this setting by its bucket's name. %q isn't a bucket name: %s.", req.ID, p))
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("bucket"), req.ID)...)
}

// what names a setting for messages: "mos_bucket_cors" is "bucket CORS rules".
func what(typeName string) string {
	switch typeName {
	case "mos_bucket_acl":
		return "bucket ACL"
	case "mos_bucket_cors":
		return "bucket CORS rules"
	case "mos_bucket_lifecycle":
		return "bucket lifecycle rules"
	case "mos_bucket_logging":
		return "bucket access logging"
	case "mos_bucket_object_lock":
		return "bucket object lock"
	}
	return typeName
}

// readError records a failed read. A missing bucket is marked so Read drops the resource.
func readError(diags *diag.Diagnostics, typeName string, err error) {
	if bucketGone(err) {
		diags.AddWarning(goneSummary, err.Error())
		return
	}
	diags.AddError("Unable to read the "+what(typeName), err.Error())
}

const goneSummary = "Bucket gone"

func bucketGoneIn(diags diag.Diagnostics) bool {
	for _, d := range diags {
		if d.Summary() == goneSummary {
			return true
		}
	}
	return false
}

// --- value helpers

// setOf is a set of strings, or null for none (so an unset optional attribute reads back the same).
func setOf(in []string) types.Set {
	if len(in) == 0 {
		return types.SetNull(types.StringType)
	}
	v := make([]attr.Value, len(in))
	for i, s := range in {
		v[i] = types.StringValue(s)
	}
	return types.SetValueMust(types.StringType, v)
}

func strsOf(s types.Set) []string {
	if s.IsNull() || s.IsUnknown() {
		return nil
	}
	out := make([]string, 0, len(s.Elements()))
	for _, e := range s.Elements() {
		if v, ok := e.(types.String); ok {
			out = append(out, v.ValueString())
		}
	}
	return out
}

// optString is null for "", so an unset optional attribute reads back the same.
func optString(s string) types.String {
	if s == "" {
		return types.StringNull()
	}
	return types.StringValue(s)
}

// optInt is null for 0.
func optInt(n int64) types.Int64 {
	if n == 0 {
		return types.Int64Null()
	}
	return types.Int64Value(n)
}
