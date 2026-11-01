package deployment

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"regexp"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/mtn/terraform-provider-mtncloud/internal/client"
)

// fileResource is one file of a file-type version, at the version's top level: MTN Cloud lists
// and deletes files only there (live QA 2026-11-01: a folder's listing and its files' deletes
// answer HTTP 500).
type fileResource struct{ base }

func newFile() resource.Resource { return &fileResource{} }

var (
	_ resource.ResourceWithImportState = (*fileResource)(nil)
	_ resource.ResourceWithModifyPlan  = (*fileResource)(nil)
)

type fileModel struct {
	ID            types.String `tfsdk:"id"`
	DeploymentID  types.String `tfsdk:"deployment_id"`
	VersionID     types.String `tfsdk:"version_id"`
	Name          types.String `tfsdk:"name"`
	Content       types.String `tfsdk:"content"`
	ContentBase64 types.String `tfsdk:"content_base64"`
	Source        types.String `tfsdk:"source"`
	ContentSHA256 types.String `tfsdk:"content_sha256"`
	SizeBytes     types.Int64  `tfsdk:"size_bytes"`
}

// fileName is a file name with no folder: MTN Cloud keeps files only at a version's top level.
var fileName = regexp.MustCompile(`^[^/\\]+$`)

// maxFile bounds what is read into memory for an upload.
const maxFile = 512 << 20

func (r *fileResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_deployment_file"
}

func (r *fileResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	one := stringvalidator.ExactlyOneOf(path.MatchRoot("content"), path.MatchRoot("content_base64"), path.MatchRoot("source"))
	resp.Schema = schema.Schema{
		Description: "A file of a deployment version whose `type` is `file`, up to 512 MiB. Changing its content uploads it " +
			"again in place. Files sit at the version's top level: MTN Cloud doesn't list or delete files in folders, so put a " +
			"folder tree in an archive file (a .zip or .tar.gz) and unpack it on the instance. Destroy deletes the file from " +
			"the version, not from instances it was deployed to.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{Computed: true, PlanModifiers: keep, Description: "`<deployment_id>/<version_id>/<name>`."},
			"deployment_id": schema.StringAttribute{Required: true, Validators: idValid, PlanModifiers: replace,
				Description: "The deployment, for example `mtncloud_deployment.web.id`."},
			"version_id": schema.StringAttribute{Required: true, Validators: idValid, PlanModifiers: replace,
				Description: "The version, for example `mtncloud_deployment_version.v1.id`. Changing it uploads the file to that version."},
			"name": schema.StringAttribute{Required: true, PlanModifiers: replace,
				Validators:  []validator.String{stringvalidator.LengthBetween(1, 255), stringvalidator.RegexMatches(fileName, "must be a file name with no folder")},
				Description: "The file's name, for example `index.html`. Changing it deletes this file and uploads one with the new name."},
			"content": schema.StringAttribute{Optional: true, Validators: []validator.String{one},
				Description: "The content, as text. Set one of `content`, `content_base64` and `source`."},
			"content_base64": schema.StringAttribute{Optional: true, Description: "The content, base64-encoded, for binary data."},
			"source": schema.StringAttribute{Optional: true,
				Description: "A local file to upload. Its content is compared at every plan, so editing the file uploads it again. A file made later in the same apply is read then."},
			"content_sha256": schema.StringAttribute{Computed: true,
				Description: "The SHA-256 of the content Terraform uploaded, in hex. Empty after an import, so the next apply " +
					"uploads the configuration's content."},
			"size_bytes": schema.Int64Attribute{Computed: true,
				Description: "The file's size in bytes. When it changes outside Terraform, the next apply uploads the file again."},
		},
	}
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// content is the bytes to upload, from whichever of the three attributes is set.
func (m *fileModel) content() ([]byte, error) {
	switch {
	case !m.Content.IsNull():
		return []byte(m.Content.ValueString()), nil
	case !m.ContentBase64.IsNull():
		b, err := base64.StdEncoding.DecodeString(m.ContentBase64.ValueString())
		if err != nil {
			return nil, fmt.Errorf("content_base64 isn't valid base64: %w", err)
		}
		return b, nil
	case !m.Source.IsNull():
		st, err := os.Stat(m.Source.ValueString())
		if err != nil {
			return nil, fmt.Errorf("source %q: %w", m.Source.ValueString(), err)
		}
		if st.Size() > maxFile {
			return nil, fmt.Errorf("source %q is %d MiB; a file here may be at most 512 MiB", m.Source.ValueString(), st.Size()>>20)
		}
		return os.ReadFile(m.Source.ValueString())
	}
	return nil, fmt.Errorf("set one of content, content_base64 and source")
}

// missing reports whether a local file doesn't exist (yet).
func missing(p string) bool {
	_, err := os.Stat(p)
	return errors.Is(err, fs.ErrNotExist)
}

// ModifyPlan uploads again only when the content differs from what was uploaded.
func (r *fileResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.Plan.Raw.IsNull() {
		return
	}
	var plan fileModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	// A source written later in the same apply (for example by local_file) is read then.
	notYet := !plan.Source.IsNull() && !plan.Source.IsUnknown() && missing(plan.Source.ValueString())
	if plan.Content.IsUnknown() || plan.ContentBase64.IsUnknown() || plan.Source.IsUnknown() || notYet {
		plan.ContentSHA256, plan.SizeBytes = types.StringUnknown(), types.Int64Unknown()
	} else {
		b, err := plan.content()
		if err != nil {
			resp.Diagnostics.AddError("Unable to read the file's content", err.Error())
			return
		}
		if len(b) > maxFile {
			resp.Diagnostics.AddError("File too large", fmt.Sprintf("The content is %d MiB; a file here may be at most 512 MiB.", len(b)>>20))
			return
		}
		plan.ContentSHA256, plan.SizeBytes = types.StringValue(sha256Hex(b)), types.Int64Value(int64(len(b)))
	}
	resp.Diagnostics.Append(resp.Plan.Set(ctx, &plan)...)
}

func (r *fileResource) upload(ctx context.Context, m *fileModel) error {
	b, err := m.content()
	if err != nil {
		return err
	}
	if err := r.client.UploadDeploymentFile(ctx, parseID(m.DeploymentID.ValueString()), parseID(m.VersionID.ValueString()), m.Name.ValueString(), b); err != nil {
		return err
	}
	m.ID = types.StringValue(m.DeploymentID.ValueString() + "/" + m.VersionID.ValueString() + "/" + m.Name.ValueString())
	m.ContentSHA256, m.SizeBytes = types.StringValue(sha256Hex(b)), types.Int64Value(int64(len(b)))
	return nil
}

func (r *fileResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	if !r.ready(&resp.Diagnostics) {
		return
	}
	var m fileModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	// A file already there may be another config's: never overwritten unasked.
	if f, err := r.find(ctx, &m); err != nil {
		resp.Diagnostics.AddError("Unable to read the version's files", err.Error())
		return
	} else if f != nil {
		resp.Diagnostics.AddAttributeError(path.Root("name"), "File already exists",
			fmt.Sprintf("Version %s already has a file %q. Import it to manage it here (terraform import <address> %s/%s/%s), or give "+
				"this one another name.", m.VersionID.ValueString(), m.Name.ValueString(), m.DeploymentID.ValueString(),
				m.VersionID.ValueString(), m.Name.ValueString()))
		return
	}
	if err := r.upload(ctx, &m); err != nil {
		resp.Diagnostics.AddError("Unable to upload "+m.Name.ValueString(), err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

// find is the file at the version's top level, nil when it isn't there.
func (r *fileResource) find(ctx context.Context, m *fileModel) (*client.DeploymentFile, error) {
	fs, err := r.client.DeploymentFiles(ctx, parseID(m.DeploymentID.ValueString()), parseID(m.VersionID.ValueString()))
	if err != nil {
		return nil, err
	}
	for i := range fs {
		if !fs[i].Directory && fs[i].Name == m.Name.ValueString() {
			return &fs[i], nil
		}
	}
	return nil, nil
}

func (r *fileResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	if !r.ready(&resp.Diagnostics) {
		return
	}
	var m fileModel
	resp.Diagnostics.Append(req.State.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	f, err := r.find(ctx, &m)
	if client.IsNotFound(err) || (err == nil && f == nil) {
		resp.State.RemoveResource(ctx) // the file, its version or its deployment is gone
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Unable to read the version's files", err.Error())
		return
	}
	// A file changed outside Terraform reads with its new size, which differs from the planned one,
	// so the plan uploads it again. (A change that keeps the size can't be seen: MTN Cloud gives no hash.)
	m.SizeBytes = types.Int64Value(f.Size)
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

func (r *fileResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	if !r.ready(&resp.Diagnostics) {
		return
	}
	var m fileModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	// Uploading the same name overwrites the file.
	if err := r.upload(ctx, &m); err != nil {
		resp.Diagnostics.AddError("Unable to upload "+m.Name.ValueString(), err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

func (r *fileResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	if !r.ready(&resp.Diagnostics) {
		return
	}
	var m fileModel
	resp.Diagnostics.Append(req.State.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.client.DeleteDeploymentFile(ctx, parseID(m.DeploymentID.ValueString()), parseID(m.VersionID.ValueString()), m.Name.ValueString()); err != nil {
		resp.Diagnostics.AddError("Unable to delete "+m.Name.ValueString(), err.Error())
	}
}

// ImportState takes <deployment_id>/<version_id>/<name>. The content isn't read back, so the next
// apply uploads the configuration's.
func (r *fileResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	m := regexp.MustCompile(`^([0-9]+)/([0-9]+)/([^/\\]+)$`).FindStringSubmatch(req.ID)
	if m == nil {
		resp.Diagnostics.AddError("Invalid import ID", fmt.Sprintf("Expected <deployment_id>/<version_id>/<name>, got %q.", req.ID))
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("deployment_id"), m[1])...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("version_id"), m[2])...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("name"), m[3])...)
}
