package storage

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/resourcevalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/mtn/terraform-provider-mtncloud/internal/client"
)

var (
	_ resource.ResourceWithConfigure        = (*archiveFileResource)(nil)
	_ resource.ResourceWithImportState      = (*archiveFileResource)(nil)
	_ resource.ResourceWithModifyPlan       = (*archiveFileResource)(nil)
	_ resource.ResourceWithConfigValidators = (*archiveFileResource)(nil)
)

// archiveFilePath is a path in an archive: no slash at either end and no empty part. dotPart finds
// a "." or ".." part, which a proxy would resolve into another path (or another archive).
var (
	archiveFilePath = regexp.MustCompile(`^[^/]+(/[^/]+)*$`)
	dotPart         = regexp.MustCompile(`(^|/)\.\.?(/|$)`)
)

// noDotParts refuses a path with a "." or ".." part.
type noDotParts struct{}

func (noDotParts) Description(context.Context) string { return "must have no . or .. part" }

func (v noDotParts) MarkdownDescription(ctx context.Context) string { return v.Description(ctx) }

func (noDotParts) ValidateString(_ context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if !req.ConfigValue.IsNull() && !req.ConfigValue.IsUnknown() && dotPart.MatchString(req.ConfigValue.ValueString()) {
		resp.Diagnostics.AddAttributeError(req.Path, "Invalid path",
			fmt.Sprintf("%q has a . or .. part. Give the file's whole path from the top of the archive, for example `docs/report.pdf`.", req.ConfigValue.ValueString()))
	}
}

func newArchiveFile() resource.Resource { return &archiveFileResource{} }

// archiveFileResource is one file in an archive. The API has no checksum, so Terraform keeps the
// SHA-256 of what it uploaded and compares the content with it at every plan.
type archiveFileResource struct{ base }

type archiveFileModel struct {
	ID            types.String `tfsdk:"id"`
	Archive       types.String `tfsdk:"archive"`
	ArchiveID     types.String `tfsdk:"archive_id"`
	Path          types.String `tfsdk:"path"`
	Content       types.String `tfsdk:"content"`
	ContentBase64 types.String `tfsdk:"content_base64"`
	Source        types.String `tfsdk:"source"`
	ContentSHA256 types.String `tfsdk:"content_sha256"`
	SizeBytes     types.Int64  `tfsdk:"size_bytes"`
	ContentType   types.String `tfsdk:"content_type"`
	LastUpdated   types.String `tfsdk:"last_updated"`
}

func (r *archiveFileResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_archive_file"
}

func (r *archiveFileResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	one := stringvalidator.ExactlyOneOf(path.MatchRoot("content"), path.MatchRoot("content_base64"), path.MatchRoot("source"))
	keep := []planmodifier.String{stringplanmodifier.UseStateForUnknown()}
	resp.Schema = schema.Schema{
		Description: "A file in an archive (Tools > Archives), up to 512 MiB, kept in the archive's MOS bucket. Changing its " +
			"content uploads it again; the file keeps its ID. Its folders are made on the way. Destroy deletes the file, " +
			"not the folders it is in.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{Description: "The file's ID.", Computed: true, PlanModifiers: keep},
			// Optional+Computed: give the archive by name or by ID; the other is filled in.
			"archive": schema.StringAttribute{
				Description: "The archive, by name (`mtncloud_archive_bucket.x.name`). Set this or `archive_id`. Another archive " +
					"replaces the file. When the archive may be renamed, use `archive_id`: a new name can't be matched to the " +
					"old one at plan time, so the file would be uploaded again.",
				Optional: true, Computed: true,
				Validators: []validator.String{stringvalidator.LengthBetween(1, 255)},
			},
			"archive_id": schema.StringAttribute{
				Description: "The archive, by ID (`mtncloud_archive_bucket.x.id`). Set this or `archive`. Another archive replaces the file.",
				Optional:    true, Computed: true,
				Validators: []validator.String{stringvalidator.RegexMatches(digits, "must be an archive ID (digits)")},
			},
			"path": schema.StringAttribute{
				Description: "The file's path in the archive, for example `reports/2026/october.pdf`, with no `/` at either " +
					"end. Changing it replaces the file.",
				Required: true,
				Validators: []validator.String{
					stringvalidator.LengthBetween(1, 1024),
					stringvalidator.RegexMatches(archiveFilePath, "must be a path such as `docs/report.pdf`, with no `/` at either end and no empty part"),
					noDotParts{},
				},
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"content":        schema.StringAttribute{Description: "The content, as text. Set one of `content`, `content_base64` and `source`.", Optional: true, Validators: []validator.String{one}},
			"content_base64": schema.StringAttribute{Description: "The content, base64-encoded, for binary data.", Optional: true},
			"source": schema.StringAttribute{
				Description: "A local file to upload. Its content is compared at every plan, so editing the file uploads it again.",
				Optional:    true,
			},
			"content_sha256": schema.StringAttribute{
				Description: "The SHA-256 of the content Terraform uploaded, in hex. Empty after the file was changed outside " +
					"Terraform (its size changed), so the next apply uploads it again.",
				Computed: true,
			},
			"size_bytes":   schema.Int64Attribute{Description: "The file's size in bytes.", Computed: true},
			"content_type": schema.StringAttribute{Description: "The content type the archive serves the file with.", Computed: true},
			"last_updated": schema.StringAttribute{Description: "When the file was last written (RFC 3339).", Computed: true},
		},
	}
}

func (r *archiveFileResource) ConfigValidators(context.Context) []resource.ConfigValidator {
	return []resource.ConfigValidator{resourcevalidator.ExactlyOneOf(path.MatchRoot("archive"), path.MatchRoot("archive_id"))}
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// ModifyPlan resolves the archive into its name and ID (D-006), replaces the file when that is
// another archive, and uploads again only when the content differs from what was uploaded.
func (r *archiveFileResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.Plan.Raw.IsNull() || r.client == nil {
		return
	}
	var cfg, plan archiveFileModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	plan.Archive, plan.ArchiveID = cfg.Archive, cfg.ArchiveID
	switch {
	case !cfg.ArchiveID.IsNull() && !cfg.ArchiveID.IsUnknown():
		id, _ := strconv.ParseInt(cfg.ArchiveID.ValueString(), 10, 64)
		a, err := r.client.GetArchiveBucket(ctx, id)
		switch {
		case client.IsNotFound(err):
			resp.Diagnostics.AddAttributeError(path.Root("archive_id"), "No such archive",
				fmt.Sprintf("No archive has ID %d. Use `mtncloud_archive_bucket.<name>.id`, or see Tools > Archives.", id))
			return
		case err != nil:
			resp.Diagnostics.AddAttributeError(path.Root("archive_id"), "Unable to read the archive", err.Error())
			return
		}
		plan.Archive = types.StringValue(a.Name)
	case !cfg.Archive.IsNull() && !cfg.Archive.IsUnknown():
		plan.ArchiveID = types.StringUnknown() // an archive made in the same run is found at create
		a, err := r.client.FindArchiveBucket(ctx, cfg.Archive.ValueString())
		if err != nil {
			resp.Diagnostics.AddAttributeError(path.Root("archive"), "Unable to look up the archive", err.Error())
			return
		}
		if a != nil {
			plan.ArchiveID = types.StringValue(strconv.FormatInt(a.ID, 10))
		}
	default:
		plan.Archive, plan.ArchiveID = types.StringUnknown(), types.StringUnknown()
	}

	var state *archiveFileModel
	if !req.State.Raw.IsNull() {
		state = &archiveFileModel{}
		resp.Diagnostics.Append(req.State.Get(ctx, state)...)
		if resp.Diagnostics.HasError() {
			return
		}
		if plan.ArchiveID.IsUnknown() || !plan.ArchiveID.Equal(state.ArchiveID) {
			resp.RequiresReplace = append(resp.RequiresReplace, path.Root("archive_id"))
		}
	}
	if plan.Content.IsUnknown() || plan.ContentBase64.IsUnknown() || plan.Source.IsUnknown() {
		plan.ContentSHA256, plan.SizeBytes = types.StringUnknown(), types.Int64Unknown()
		plan.ContentType, plan.LastUpdated = types.StringUnknown(), types.StringUnknown()
		resp.Diagnostics.Append(resp.Plan.Set(ctx, &plan)...)
		return
	}
	b, err := contentOf(plan.Content, plan.ContentBase64, plan.Source, "a file")
	if err != nil {
		resp.Diagnostics.AddError("Unable to read the file's content", err.Error())
		return
	}
	if len(b) > maxUpload {
		resp.Diagnostics.AddError("File too large", fmt.Sprintf("The content is %d MiB; a file here may be at most 512 MiB.", len(b)>>20))
		return
	}
	plan.ContentSHA256, plan.SizeBytes = types.StringValue(sha256Hex(b)), types.Int64Value(int64(len(b)))
	if state != nil && plan.ContentSHA256.Equal(state.ContentSHA256) && len(resp.RequiresReplace) == 0 {
		plan.ContentType, plan.LastUpdated = state.ContentType, state.LastUpdated // nothing to upload
	} else {
		plan.ContentType, plan.LastUpdated = types.StringUnknown(), types.StringUnknown()
	}
	resp.Diagnostics.Append(resp.Plan.Set(ctx, &plan)...)
}

// archiveName is the archive's name at apply time, for an archive given by ID or made after the plan.
func (r *archiveFileResource) archiveName(ctx context.Context, m *archiveFileModel, diags *diag.Diagnostics) string {
	if !m.Archive.IsUnknown() && !m.Archive.IsNull() {
		return m.Archive.ValueString()
	}
	id, _ := strconv.ParseInt(m.ArchiveID.ValueString(), 10, 64)
	a, err := r.client.GetArchiveBucket(ctx, id)
	if err != nil {
		diags.AddAttributeError(path.Root("archive_id"), "Unable to read the archive", err.Error())
		return ""
	}
	return a.Name
}

func (r *archiveFileResource) fill(m *archiveFileModel, f *client.ArchiveFile, b []byte) {
	m.ID = types.StringValue(strconv.FormatInt(f.ID, 10))
	// A short answer (an upload whose read-back failed) keeps what was planned.
	if f.ArchiveID != 0 {
		m.Archive, m.ArchiveID = types.StringValue(f.Archive), types.StringValue(strconv.FormatInt(f.ArchiveID, 10))
	}
	if f.FilePath != "" {
		m.Path = types.StringValue(f.FilePath)
	}
	m.ContentType, m.LastUpdated = types.StringValue(f.ContentType), types.StringValue(f.LastUpdated)
	if b != nil { // just uploaded: what Terraform sent
		m.ContentSHA256, m.SizeBytes = types.StringValue(sha256Hex(b)), types.Int64Value(int64(len(b)))
		return
	}
	if m.SizeBytes.IsNull() || m.SizeBytes.ValueInt64() != f.RawSize {
		m.ContentSHA256 = types.StringValue("") // changed outside Terraform, or imported: upload again
	}
	m.SizeBytes = types.Int64Value(f.RawSize)
}

// upload writes the planned content and reads the file back, for its type and time. was is the
// file's last write time before this upload ("" on create, when the path was empty).
func (r *archiveFileResource) upload(ctx context.Context, m *archiveFileModel, archive, was string, diags *diag.Diagnostics) {
	b, err := contentOf(m.Content, m.ContentBase64, m.Source, "a file")
	if err != nil {
		diags.AddError("Unable to read the file's content", err.Error())
		return
	}
	p := m.Path.ValueString()
	f, err := r.client.UploadArchiveFile(ctx, archive, p, b)
	if err != nil && client.MayHaveSucceeded(err) {
		// The answer was lost: a file of this size at this path, written since was, is ours. On an
		// update the old file is always there, so its write time must have moved.
		if found, ferr := r.client.FindArchiveFile(ctx, archive, p); ferr == nil && found != nil && found.RawSize == int64(len(b)) &&
			(was == "" || (found.LastUpdated != "" && found.LastUpdated != was)) {
			f, err = found, nil
		}
	}
	if err != nil {
		diags.AddError("Unable to upload the file", fmt.Sprintf("Uploading %s to archive %q: %s", p, archive, err))
		return
	}
	if full, gerr := r.client.GetArchiveFile(ctx, f.ID); gerr == nil {
		f = full // the upload answer has no size, type or time
	}
	r.fill(m, f, b)
}

func (r *archiveFileResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	if !r.ready(&resp.Diagnostics) {
		return
	}
	var plan archiveFileModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	archive := r.archiveName(ctx, &plan, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	// An upload overwrites: never take over a file Terraform didn't make.
	p := plan.Path.ValueString()
	if found, err := r.client.FindArchiveFile(ctx, archive, p); err != nil {
		resp.Diagnostics.AddError("Unable to check for an existing file", err.Error())
		return
	} else if found != nil {
		resp.Diagnostics.AddAttributeError(path.Root("path"), "File already exists",
			fmt.Sprintf("Archive %q already has a file at %s (ID %d). Import it with `terraform import <address> %d`, or choose another path.", archive, p, found.ID, found.ID))
		return
	}
	r.upload(ctx, &plan, archive, "", &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *archiveFileResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	if !r.ready(&resp.Diagnostics) {
		return
	}
	var state archiveFileModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	id, _ := strconv.ParseInt(state.ID.ValueString(), 10, 64)
	f, err := r.client.GetArchiveFile(ctx, id)
	switch {
	case client.IsNotFound(err):
		resp.State.RemoveResource(ctx)
		return
	case err != nil:
		resp.Diagnostics.AddError("Unable to read the file", err.Error())
		return
	}
	r.fill(&state, f, nil)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *archiveFileResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	if !r.ready(&resp.Diagnostics) {
		return
	}
	var plan, state archiveFileModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if plan.ContentSHA256.Equal(state.ContentSHA256) {
		// Only how the content is given changed (content to source, say), not the content.
		plan.SizeBytes, plan.ContentType, plan.LastUpdated = state.SizeBytes, state.ContentType, state.LastUpdated
		resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
		return
	}
	plan.ID = state.ID
	r.upload(ctx, &plan, r.archiveName(ctx, &plan, &resp.Diagnostics), state.LastUpdated.ValueString(), &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *archiveFileResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	if !r.ready(&resp.Diagnostics) {
		return
	}
	var state archiveFileModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	id, _ := strconv.ParseInt(state.ID.ValueString(), 10, 64)
	if err := r.client.DeleteArchiveFile(ctx, id); err != nil {
		resp.Diagnostics.AddError("Unable to delete the file", err.Error())
	}
}

// ImportState takes the file's ID, or `<archive>/<path>`.
func (r *archiveFileResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	if !r.ready(&resp.Diagnostics) {
		return
	}
	if digits.MatchString(req.ID) {
		resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
		return
	}
	archive, p, ok := strings.Cut(req.ID, "/")
	if !ok || archive == "" || p == "" {
		resp.Diagnostics.AddError("Invalid import ID", fmt.Sprintf("Import a file by its ID or as `<archive>/<path>`, for example `exports/docs/report.pdf`; got %q.", req.ID))
		return
	}
	f, err := r.client.FindArchiveFile(ctx, archive, p)
	switch {
	case err != nil:
		resp.Diagnostics.AddError("Unable to find the file", err.Error())
	case f == nil:
		resp.Diagnostics.AddError("No such file", fmt.Sprintf("Archive %q has no file at %s.", archive, p))
	default:
		resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), strconv.FormatInt(f.ID, 10))...)
	}
}
