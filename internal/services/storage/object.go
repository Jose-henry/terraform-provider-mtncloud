package storage

import (
	"context"
	"crypto/md5" //nolint:gosec // MOS's ETag is the MD5 of a single-part upload: compared, not trusted for security
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/mtn/terraform-provider-mtncloud/internal/client"
)

var (
	_ resource.ResourceWithImportState = (*objectResource)(nil)
	_ resource.ResourceWithModifyPlan  = (*objectResource)(nil)
)

// maxUpload caps what one object may hold: the provider reads it into memory to upload it in one
// request.
const maxUpload = 512 << 20

func newObject() resource.Resource { return &objectResource{} }

type objectResource struct{ base }

type objectModel struct {
	ID            types.String `tfsdk:"id"`
	Bucket        types.String `tfsdk:"bucket"`
	Key           types.String `tfsdk:"key"`
	Content       types.String `tfsdk:"content"`
	ContentBase64 types.String `tfsdk:"content_base64"`
	Source        types.String `tfsdk:"source"`
	Metadata      types.Map    `tfsdk:"metadata"`
	Checksum      types.String `tfsdk:"checksum_algorithm"`
	RetentionMode types.String `tfsdk:"retention_mode"`
	RetainUntil   types.String `tfsdk:"retain_until"`
	Bypass        types.Bool   `tfsdk:"bypass_governance_on_destroy"`
	ETag          types.String `tfsdk:"etag"`
	Size          types.Int64  `tfsdk:"size"`
	LastModified  types.String `tfsdk:"last_modified"`
}

func (r *objectResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_mos_object"
}

func (r *objectResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	one := stringvalidator.ExactlyOneOf(path.MatchRoot("content"), path.MatchRoot("content_base64"), path.MatchRoot("source"))
	keep := []planmodifier.String{stringplanmodifier.UseStateForUnknown()}
	resp.Schema = schema.Schema{
		Description: "A file (object) in a MOS bucket, up to 512 MiB. Changing its content uploads it again. MOS " +
			"doesn't keep the content type an upload sets: every object is served as `binary/octet-stream`.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{Description: "`<bucket>/<key>`.", Computed: true, PlanModifiers: keep},
			"bucket": schema.StringAttribute{Description: "The bucket's name.", Required: true, Validators: bucketNameValidators(),
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()}},
			"key": schema.StringAttribute{
				Description: "The object's name in the bucket, for example `site/index.html`. Changing it replaces the object.",
				Required:    true, Validators: []validator.String{stringvalidator.LengthBetween(1, 1024)},
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"content":        schema.StringAttribute{Description: "The content, as text. Set one of `content`, `content_base64` and `source`.", Optional: true, Validators: []validator.String{one}},
			"content_base64": schema.StringAttribute{Description: "The content, base64-encoded, for binary data.", Optional: true},
			"source": schema.StringAttribute{
				Description: "A local file to upload. Its content is compared at every plan, so editing the file uploads it again.",
				Optional:    true,
			},
			"metadata": schema.MapAttribute{
				Description: "Your own metadata, stored with the object as user metadata. Names are lower case.",
				ElementType: types.StringType, Optional: true,
			},
			"checksum_algorithm": schema.StringAttribute{
				Description: "Have MOS check the upload with this checksum: `CRC32`, `CRC32C`, `SHA1` or `SHA256`. Needs " +
					"checksums to be enabled on MTN Object Storage; the upload fails with a clear message when they aren't.",
				Optional: true, Validators: []validator.String{stringvalidator.OneOf("CRC32", "CRC32C", "SHA1", "SHA256")},
			},
			"retention_mode": schema.StringAttribute{
				Description: "Keep the object from deletion until `retain_until`: `GOVERNANCE`. The bucket must have object lock on.",
				Optional:    true, Validators: []validator.String{stringvalidator.OneOf("GOVERNANCE"), stringvalidator.AlsoRequires(path.MatchRoot("retain_until"))},
			},
			"retain_until": schema.StringAttribute{
				Description: "The date and time the retention ends, in RFC 3339 (`2027-01-31T00:00:00Z`). Moving it later is " +
					"always allowed; moving it earlier needs `bypass_governance_on_destroy = true`.",
				Optional: true, Validators: []validator.String{stringvalidator.AlsoRequires(path.MatchRoot("retention_mode"))},
			},
			// Optional+Computed with a default: a Terraform-only switch, never stored in MOS.
			"bypass_governance_on_destroy": schema.BoolAttribute{
				Description: "Delete the object on destroy, and shorten its retention, even while GOVERNANCE retention holds it. Needs permission to bypass it.",
				Optional:    true, Computed: true, Default: booldefault.StaticBool(false),
			},
			"etag":          schema.StringAttribute{Description: "The object's ETag: the MD5 of its content for an object uploaded in one part.", Computed: true},
			"size":          schema.Int64Attribute{Description: "The object's size in bytes.", Computed: true},
			"last_modified": schema.StringAttribute{Description: "When the object was last written (RFC 3339).", Computed: true},
		},
	}
}

// body is the object's content from whichever attribute holds it.
func (m *objectModel) body() ([]byte, error) {
	return contentOf(m.Content, m.ContentBase64, m.Source, "an object")
}

// contentOf is a file's content from whichever of content, content_base64 and source holds it.
// what names the thing in the size error ("an object").
func contentOf(content, contentBase64, source types.String, what string) ([]byte, error) {
	switch {
	case !content.IsNull():
		return []byte(content.ValueString()), nil
	case !contentBase64.IsNull():
		b, err := base64.StdEncoding.DecodeString(contentBase64.ValueString())
		if err != nil {
			return nil, fmt.Errorf("content_base64 isn't valid base64: %w", err)
		}
		return b, nil
	}
	name := source.ValueString()
	st, err := os.Stat(name)
	if err != nil {
		return nil, fmt.Errorf("source: %w", err)
	}
	if st.Size() > maxUpload {
		return nil, fmt.Errorf("source %s is %d MiB; %s here may be at most 512 MiB", name, st.Size()>>20, what)
	}
	return os.ReadFile(name) //nolint:gosec // a file the configuration names, as the user means
}

func md5Hex(b []byte) string {
	sum := md5.Sum(b) //nolint:gosec // see the import
	return hex.EncodeToString(sum[:])
}

// ModifyPlan uploads again when the content no longer matches what MOS holds: a source file edited
// since the last apply, or the object overwritten outside Terraform (Read records its new ETag).
func (r *objectResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.Plan.Raw.IsNull() || req.State.Raw.IsNull() {
		return
	}
	var plan, state objectModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() || plan.Content.IsUnknown() || plan.ContentBase64.IsUnknown() || plan.Source.IsUnknown() {
		return
	}
	b, err := plan.body()
	if err != nil {
		resp.Diagnostics.AddError("Unable to read the object's content", err.Error())
		return
	}
	if len(b) > maxUpload {
		resp.Diagnostics.AddError("Object too large", fmt.Sprintf("The content is %d MiB; an object here may be at most 512 MiB.", len(b)>>20))
		return
	}
	// The Framework marks every computed value unknown when anything changes; Update reads an unknown
	// etag as "upload again". So the content decides: unchanged content keeps what MOS holds, and a
	// change to retention or bypass alone never rewrites the object.
	if md5Hex(b) != state.ETag.ValueString() {
		resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("etag"), types.StringUnknown())...)
		resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("size"), types.Int64Unknown())...)
		resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("last_modified"), types.StringUnknown())...)
		return
	}
	if !plan.Metadata.Equal(state.Metadata) || !plan.Checksum.Equal(state.Checksum) {
		return // uploaded again with the new metadata or checksum: last_modified changes
	}
	resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("etag"), state.ETag)...)
	resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("size"), state.Size)...)
	resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("last_modified"), state.LastModified)...)
}

func (r *objectResource) input(ctx context.Context, m *objectModel, diags *diag.Diagnostics) (client.MOSObjectInput, bool) {
	in := client.MOSObjectInput{Bucket: m.Bucket.ValueString(), Key: m.Key.ValueString(), ChecksumAlgorithm: m.Checksum.ValueString()}
	b, err := m.body()
	if err != nil {
		diags.AddError("Unable to read the object's content", err.Error())
		return in, false
	}
	in.Body = b
	if !m.Metadata.IsNull() {
		meta := map[string]string{}
		diags.Append(m.Metadata.ElementsAs(ctx, &meta, false)...)
		for k := range meta {
			if k != strings.ToLower(k) {
				diags.AddAttributeError(path.Root("metadata"), "Metadata name has capitals",
					fmt.Sprintf("MOS stores metadata names in lower case, so %q would read back as %q. Use %q.", k, strings.ToLower(k), strings.ToLower(k)))
			}
		}
		in.Metadata = meta
	}
	if !m.RetentionMode.IsNull() {
		t, err := time.Parse(time.RFC3339, m.RetainUntil.ValueString())
		if err != nil {
			diags.AddAttributeError(path.Root("retain_until"), "Invalid retain_until", fmt.Sprintf("%q isn't an RFC 3339 time such as 2027-01-31T00:00:00Z.", m.RetainUntil.ValueString()))
			return in, false
		}
		in.LockMode, in.RetainUntil = m.RetentionMode.ValueString(), t
	}
	return in, !diags.HasError()
}

func (r *objectResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	if !r.ready(&resp.Diagnostics) {
		return
	}
	var plan objectModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	r.upload(ctx, &plan, "", &resp.Diagnostics)
	if !resp.Diagnostics.HasError() {
		resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
	}
}

func (r *objectResource) upload(ctx context.Context, m *objectModel, acl string, diags *diag.Diagnostics) {
	in, ok := r.input(ctx, m, diags)
	if !ok {
		return
	}
	in.ACL = acl
	ctx, cancel := context.WithTimeout(ctx, mosTimeout)
	defer cancel()
	o, err := r.client.PutMOSObject(ctx, in)
	if err != nil {
		diags.AddError("Unable to upload the object", err.Error())
		return
	}
	m.ID = types.StringValue(in.Bucket + "/" + in.Key)
	m.ETag, m.Size, m.LastModified = types.StringValue(o.ETag), types.Int64Value(o.Size), types.StringValue(o.LastModified)
}

func (r *objectResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	if !r.ready(&resp.Diagnostics) {
		return
	}
	var state objectModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	o, err := r.client.HeadMOSObject(ctx, state.Bucket.ValueString(), state.Key.ValueString())
	if client.IsMOSNotFound(err) {
		resp.State.RemoveResource(ctx) // deleted outside Terraform (or its bucket was): plan a new one
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Unable to read the object", err.Error())
		return
	}
	state.ID = types.StringValue(o.Bucket + "/" + o.Key)
	state.ETag, state.Size, state.LastModified = types.StringValue(o.ETag), types.Int64Value(o.Size), types.StringValue(o.LastModified)
	if len(o.Metadata) > 0 || !state.Metadata.IsNull() {
		state.Metadata, _ = types.MapValueFrom(ctx, types.StringType, o.Metadata)
	}
	if o.LockMode == "" || (state.RetentionMode.IsNull() && expired(o.RetainUntil)) {
		// No retention, or one that has ended and was taken out of the configuration.
		state.RetentionMode, state.RetainUntil = types.StringNull(), types.StringNull()
	} else {
		state.RetentionMode = types.StringValue(o.LockMode)
		// Keep the configuration's spelling of the same instant (a +01:00 offset, say).
		if !sameInstant(state.RetainUntil.ValueString(), o.RetainUntil) {
			state.RetainUntil = types.StringValue(o.RetainUntil)
		}
	}
	if state.Bypass.IsNull() {
		state.Bypass = types.BoolValue(false)
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func sameInstant(a, b string) bool {
	x, errA := time.Parse(time.RFC3339, a)
	y, errB := time.Parse(time.RFC3339, b)
	return errA == nil && errB == nil && x.Equal(y)
}

// expired reports a retain_until date that has passed: the retention no longer holds the object.
func expired(until string) bool {
	t, err := time.Parse(time.RFC3339, until)
	return err == nil && t.Before(time.Now())
}

func (r *objectResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	if !r.ready(&resp.Diagnostics) {
		return
	}
	var plan, state objectModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	reupload := plan.ETag.IsUnknown() || !plan.Metadata.Equal(state.Metadata) || !plan.Checksum.Equal(state.Checksum)
	retention := !plan.RetentionMode.Equal(state.RetentionMode) || !sameInstant(plan.RetainUntil.ValueString(), state.RetainUntil.ValueString())
	if !plan.RetentionMode.IsNull() && retention && !state.RetentionMode.IsNull() {
		if err := r.checkShorter(plan, state); err != nil {
			resp.Diagnostics.AddAttributeError(path.Root("retain_until"), "Retention can't be shortened", err.Error())
			return
		}
	}
	if state.RetentionMode.ValueString() != "" && plan.RetentionMode.IsNull() && !expired(state.RetainUntil.ValueString()) {
		resp.Diagnostics.AddAttributeError(path.Root("retention_mode"), "Retention can't be removed",
			"GOVERNANCE retention can't be taken off an object; it ends at retain_until. Leave retention_mode and retain_until set, or move retain_until.")
		return
	}
	switch {
	case reupload:
		// A new upload carries the retention with it. Overwriting an object under retention writes a
		// new version on S3, but MOS keeps no versions, so it is refused there until retention ends.
		// An upload also resets the object's ACL to private, so the current one goes with it: an
		// mtncloud_mos_object_acl on this object stays as it was set.
		acl, err := r.client.GetObjectACL(ctx, plan.Bucket.ValueString(), plan.Key.ValueString())
		if err != nil && !client.IsMOSNotFound(err) {
			resp.Diagnostics.AddError("Unable to read the object's ACL before uploading it again", err.Error())
			return
		}
		keep := ""
		if acl.Canned != "private" && acl.Canned != "custom" {
			keep = acl.Canned
		}
		if acl.Canned == "custom" {
			resp.Diagnostics.AddWarning("Object ACL reset",
				fmt.Sprintf("%s had grants set outside Terraform that aren't a canned ACL. Uploading it again resets them to private.", plan.ID.ValueString()))
		}
		r.upload(ctx, &plan, keep, &resp.Diagnostics)
	case retention && plan.RetentionMode.IsNull():
		// An ended retention taken out of the configuration: nothing to change in MOS.
		plan.ETag, plan.Size, plan.LastModified = state.ETag, state.Size, state.LastModified
	case retention:
		t, err := time.Parse(time.RFC3339, plan.RetainUntil.ValueString())
		if err != nil {
			resp.Diagnostics.AddAttributeError(path.Root("retain_until"), "Invalid retain_until", err.Error())
			return
		}
		ctx, cancel := context.WithTimeout(ctx, mosTimeout)
		defer cancel()
		if err := r.client.PutObjectRetention(ctx, plan.Bucket.ValueString(), plan.Key.ValueString(), plan.RetentionMode.ValueString(), t, plan.Bypass.ValueBool()); err != nil {
			resp.Diagnostics.AddError("Unable to change the object's retention", err.Error())
			return
		}
		plan.ETag, plan.Size, plan.LastModified = state.ETag, state.Size, state.LastModified
	default:
		plan.ETag, plan.Size, plan.LastModified = state.ETag, state.Size, state.LastModified
	}
	if !resp.Diagnostics.HasError() {
		plan.ID = state.ID
		resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
	}
}

// checkShorter refuses to move a retention date earlier unless bypassing is on.
func (r *objectResource) checkShorter(plan, state objectModel) error {
	// A date that doesn't parse is reported where it is used, not here.
	newT, errNew := time.Parse(time.RFC3339, plan.RetainUntil.ValueString())
	oldT, errOld := time.Parse(time.RFC3339, state.RetainUntil.ValueString())
	if bad := errNew != nil || errOld != nil; bad || !newT.Before(oldT) || plan.Bypass.ValueBool() {
		return nil
	}
	return fmt.Errorf("retain_until moves from %s to %s, earlier. GOVERNANCE retention can only be shortened with "+
		"bypass_governance_on_destroy = true (and permission to bypass it)", state.RetainUntil.ValueString(), plan.RetainUntil.ValueString())
}

func (r *objectResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	if !r.ready(&resp.Diagnostics) {
		return
	}
	var state objectModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, mosTimeout)
	defer cancel()
	if err := r.client.DeleteMOSObject(ctx, state.Bucket.ValueString(), state.Key.ValueString(), state.Bypass.ValueBool()); err != nil {
		msg := err.Error()
		if state.RetentionMode.ValueString() != "" && !state.Bypass.ValueBool() {
			msg += fmt.Sprintf(" The object is under GOVERNANCE retention until %s: wait until then, or set bypass_governance_on_destroy = true and apply before destroying.", state.RetainUntil.ValueString())
		}
		resp.Diagnostics.AddError("Unable to delete the object", msg)
	}
}

// ImportState takes `<bucket>/<key>`. The content isn't read back: set content or source to match
// it, and the next plan uploads it again only if it differs.
func (r *objectResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	bucket, key, ok := strings.Cut(req.ID, "/")
	if !ok || key == "" || bucketNameProblem(bucket) != "" {
		resp.Diagnostics.AddError("Invalid import ID", fmt.Sprintf("Import an object as <bucket>/<key>, for example my-bucket/site/index.html. Got %q.", req.ID))
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("bucket"), bucket)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("key"), key)...)
}
