package storage

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/mtn/terraform-provider-mtncloud/internal/client"
)

var (
	_ resource.ResourceWithConfigure   = (*archiveLinkResource)(nil)
	_ resource.ResourceWithImportState = (*archiveLinkResource)(nil)
)

func newArchiveLink() resource.Resource { return &archiveLinkResource{} }

// archiveLinkResource is a download link to an archive file. The API can't change a link, so
// every argument replaces it.
type archiveLinkResource struct{ base }

type archiveLinkModel struct {
	ID            types.String `tfsdk:"id"`
	FileID        types.String `tfsdk:"file_id"`
	ExpireSeconds types.Int64  `tfsdk:"expire_seconds"`
	LinkID        types.String `tfsdk:"link_id"`
	Secret        types.String `tfsdk:"secret_access_key"`
	DownloadURL   types.String `tfsdk:"download_url"`
	ExpiresAt     types.String `tfsdk:"expires_at"`
	DownloadCount types.Int64  `tfsdk:"download_count"`
}

func (r *archiveLinkResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_archive_file_link"
}

func (r *archiveLinkResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	keep := []planmodifier.String{stringplanmodifier.UseStateForUnknown()}
	resp.Schema = schema.Schema{
		Description: "A download link to an archive file: anyone with its URL can download the file **without logging in**, " +
			"until it expires. The link's secret is kept in the Terraform state (sensitive). Once the link has expired or " +
			"was deleted in the console, the next plan makes a new one; an expired link stays listed in the console, where " +
			"it no longer works. Changing an argument replaces the link.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{Description: "`<file_id>/<link_id>`.", Computed: true, PlanModifiers: keep},
			"file_id": schema.StringAttribute{
				Description: "The file's ID (`mtncloud_archive_file.x.id`).", Required: true,
				Validators:    []validator.String{stringvalidator.RegexMatches(digits, "must be an archive file ID (digits)")},
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"expire_seconds": schema.Int64Attribute{
				Description: "How long the link works, in seconds, from when it is made. `0` means it never expires: anyone " +
					"who ever gets the URL can download the file until the link is destroyed.",
				Required:      true,
				Validators:    []validator.Int64{int64validator.AtLeast(0)},
				PlanModifiers: []planmodifier.Int64{int64planmodifier.RequiresReplace()},
			},
			"link_id":           schema.StringAttribute{Description: "The link's ID.", Computed: true, PlanModifiers: keep},
			"secret_access_key": schema.StringAttribute{Description: "The link's secret.", Computed: true, Sensitive: true, PlanModifiers: keep},
			"download_url": schema.StringAttribute{
				Description: "The public download URL, with the secret in it.", Computed: true, Sensitive: true, PlanModifiers: keep,
			},
			"expires_at":     schema.StringAttribute{Description: "When the link expires (RFC 3339); empty when it never does.", Computed: true, PlanModifiers: keep},
			"download_count": schema.Int64Attribute{Description: "How many times the link was used.", Computed: true},
		},
	}
}

func (r *archiveLinkResource) fill(m *archiveLinkModel, l *client.ArchiveFileLink) {
	fid, lid := strconv.FormatInt(l.FileID, 10), strconv.FormatInt(l.ID, 10)
	m.ID, m.FileID, m.LinkID = types.StringValue(fid+"/"+lid), types.StringValue(fid), types.StringValue(lid)
	m.Secret, m.DownloadURL = types.StringValue(l.Secret), types.StringValue(r.client.ArchiveLinkURL(l.Secret))
	m.ExpiresAt, m.DownloadCount = types.StringValue(l.ExpirationDate), types.Int64Value(l.DownloadCount)
}

// linkExpired says whether a link's expiry has passed. An expiry that can't be read counts as not passed.
func linkExpired(l *client.ArchiveFileLink, now time.Time) bool {
	t, err := time.Parse(time.RFC3339, l.ExpirationDate)
	return err == nil && !now.Before(t)
}

func (r *archiveLinkResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	if !r.ready(&resp.Diagnostics) {
		return
	}
	var plan archiveLinkModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	fid, _ := strconv.ParseInt(plan.FileID.ValueString(), 10, 64)
	l, err := r.client.CreateArchiveFileLink(ctx, fid, plan.ExpireSeconds.ValueInt64())
	if err != nil {
		resp.Diagnostics.AddError("Unable to create the link", fmt.Sprintf("A link to archive file %d: %s", fid, err))
		return
	}
	r.fill(&plan, l)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *archiveLinkResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	if !r.ready(&resp.Diagnostics) {
		return
	}
	var state archiveLinkModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	fid, _ := strconv.ParseInt(state.FileID.ValueString(), 10, 64)
	lid, _ := strconv.ParseInt(state.LinkID.ValueString(), 10, 64)
	links, err := r.client.ListArchiveFileLinks(ctx, fid)
	switch {
	case client.IsNotFound(err):
		resp.State.RemoveResource(ctx) // the file is gone, and its links with it
		return
	case err != nil:
		resp.Diagnostics.AddError("Unable to read the file's links", err.Error())
		return
	}
	for _, l := range links {
		if l.ID != lid {
			continue
		}
		if linkExpired(l, time.Now()) {
			break // an expired link no longer works: make a new one
		}
		r.fill(&state, l)
		if state.ExpireSeconds.IsNull() { // imported
			state.ExpireSeconds = types.Int64Value(lifetime(l))
		}
		resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
		return
	}
	resp.State.RemoveResource(ctx)
}

// lifetime is a link's expire_seconds, worked out from its dates: 0 when it never expires.
func lifetime(l *client.ArchiveFileLink) int64 {
	created, err1 := time.Parse(time.RFC3339, l.DateCreated)
	expires, err2 := time.Parse(time.RFC3339, l.ExpirationDate)
	if err1 != nil || err2 != nil {
		return 0
	}
	return int64(expires.Sub(created).Round(time.Second) / time.Second)
}

func (r *archiveLinkResource) Update(_ context.Context, _ resource.UpdateRequest, resp *resource.UpdateResponse) {
	resp.Diagnostics.AddError("Links can't change", "Every argument of mtncloud_archive_file_link replaces the link. This is a provider bug: please report it.")
}

func (r *archiveLinkResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	if !r.ready(&resp.Diagnostics) {
		return
	}
	var state archiveLinkModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	fid, _ := strconv.ParseInt(state.FileID.ValueString(), 10, 64)
	lid, _ := strconv.ParseInt(state.LinkID.ValueString(), 10, 64)
	if err := r.client.DeleteArchiveFileLink(ctx, fid, lid); err != nil {
		resp.Diagnostics.AddError("Unable to delete the link", err.Error())
	}
}

// ImportState takes `<file_id>/<link_id>`.
func (r *archiveLinkResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	fid, lid, ok := strings.Cut(req.ID, "/")
	if !ok || !digits.MatchString(fid) || !digits.MatchString(lid) {
		resp.Diagnostics.AddError("Invalid import ID", fmt.Sprintf("Import a link as `<file_id>/<link_id>`, for example `1626/8`; got %q.", req.ID))
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("file_id"), fid)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("link_id"), lid)...)
}
