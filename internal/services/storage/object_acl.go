package storage

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-framework-timeouts/resource/timeouts"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/mtn/terraform-provider-mtncloud/internal/client"
)

var _ resource.ResourceWithImportState = (*objectACLResource)(nil)

func newObjectACL() resource.Resource { return &objectACLResource{} }

type objectACLResource struct{ base }

type objectACLModel struct {
	ID     types.String `tfsdk:"id"`
	Bucket types.String `tfsdk:"bucket"`
	Key    types.String `tfsdk:"key"`
	ACL    types.String `tfsdk:"acl"`
	Grants types.List   `tfsdk:"grants"`
}

func (r *objectACLResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_mos_object_acl"
}

func (r *objectACLResource) Schema(ctx context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = withTimeouts(ctx, r.core(), timeouts.Opts{Create: true, Update: true, Delete: true})
}

// core is the schema without the timeouts block: the shape of objectACLModel, which the data source shares.
func (r *objectACLResource) core() schema.Schema {
	replace := []planmodifier.String{stringplanmodifier.RequiresReplace()}
	return schema.Schema{
		Description: "Who may read one object in a MOS bucket, as a canned ACL, for example to publish one file. " +
			"Destroy sets it back to `private`. Uploading new content with `mtncloud_mos_object` keeps it, but replacing " +
			"the object deletes it with its ACL: add `lifecycle { replace_triggered_by = [mtncloud_mos_object.<name>] }` to set it again in the same apply.",
		Attributes: map[string]schema.Attribute{
			"id":     schema.StringAttribute{Description: "`<bucket>/<key>`.", Computed: true, PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
			"bucket": schema.StringAttribute{Description: "The bucket's name.", Required: true, Validators: bucketNameValidators(), PlanModifiers: replace},
			"key": schema.StringAttribute{Description: "The object's name.", Required: true, PlanModifiers: replace,
				Validators: []validator.String{stringvalidator.LengthBetween(1, 1024)}},
			"acl": schema.StringAttribute{
				Description: "`private`, `public-read` (anyone may download it), `public-read-write` or `authenticated-read` (any MOS user may).",
				Required:    true, Validators: []validator.String{stringvalidator.OneOf(client.CannedACLs()...)},
			},
			"grants": schema.ListAttribute{Description: "The grants MOS holds, as `<grantee>:<permission>`.", ElementType: types.StringType, Computed: true},
		},
	}
}

func (r *objectACLResource) write(ctx context.Context, m *objectACLModel, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if err := r.client.PutObjectACL(ctx, m.Bucket.ValueString(), m.Key.ValueString(), m.ACL.ValueString()); err != nil {
		return err
	}
	return r.refresh(ctx, m)
}

func (r *objectACLResource) refresh(ctx context.Context, m *objectACLModel) error {
	a, err := r.client.GetObjectACL(ctx, m.Bucket.ValueString(), m.Key.ValueString())
	if err != nil {
		return err
	}
	m.ID = types.StringValue(m.Bucket.ValueString() + "/" + m.Key.ValueString())
	m.ACL = types.StringValue(a.Canned)
	m.Grants, _ = types.ListValueFrom(ctx, types.StringType, a.Grants)
	return nil
}

func (r *objectACLResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	if !r.ready(&resp.Diagnostics) {
		return
	}
	var m objectACLModel
	to, toRaw, d := coreGet(ctx, r.core(), tfsdk.State(req.Plan), &m)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}
	timeout, d := to.Create(ctx, mosTimeout)
	resp.Diagnostics.Append(d...)
	if err := r.write(ctx, &m, timeout); err != nil {
		resp.Diagnostics.AddError("Unable to set the object ACL", err.Error())
		return
	}
	resp.Diagnostics.Append(coreSet(ctx, r.core(), &resp.State, &m, toRaw)...)
}

func (r *objectACLResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	if !r.ready(&resp.Diagnostics) {
		return
	}
	var m objectACLModel
	_, toRaw, d := coreGet(ctx, r.core(), req.State, &m)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}
	err := r.refresh(ctx, &m)
	if client.IsMOSNotFound(err) {
		resp.State.RemoveResource(ctx) // the object is gone, and its ACL with it
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Unable to read the object ACL", err.Error())
		return
	}
	resp.Diagnostics.Append(coreSet(ctx, r.core(), &resp.State, &m, toRaw)...)
}

func (r *objectACLResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	if !r.ready(&resp.Diagnostics) {
		return
	}
	var m objectACLModel
	to, toRaw, d := coreGet(ctx, r.core(), tfsdk.State(req.Plan), &m)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}
	timeout, d := to.Update(ctx, mosTimeout)
	resp.Diagnostics.Append(d...)
	if err := r.write(ctx, &m, timeout); err != nil {
		resp.Diagnostics.AddError("Unable to change the object ACL", err.Error())
		return
	}
	resp.Diagnostics.Append(coreSet(ctx, r.core(), &resp.State, &m, toRaw)...)
}

func (r *objectACLResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	if !r.ready(&resp.Diagnostics) {
		return
	}
	var m objectACLModel
	to, _, d := coreGet(ctx, r.core(), req.State, &m)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}
	timeout, d := to.Delete(ctx, mosTimeout)
	resp.Diagnostics.Append(d...)
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if err := r.client.PutObjectACL(ctx, m.Bucket.ValueString(), m.Key.ValueString(), "private"); err != nil && !client.IsMOSNotFound(err) {
		resp.Diagnostics.AddError("Unable to reset the object ACL", err.Error())
	}
}

func (r *objectACLResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	bucket, key, ok := strings.Cut(req.ID, "/")
	if !ok || key == "" || bucketNameProblem(bucket) != "" {
		resp.Diagnostics.AddError("Invalid import ID", fmt.Sprintf("Import an object ACL as <bucket>/<key>. Got %q.", req.ID))
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("bucket"), bucket)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("key"), key)...)
}
