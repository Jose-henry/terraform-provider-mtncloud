package storage

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/datasourcevalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	ds "github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var _ datasource.DataSourceWithConfigValidators = (*archiveFilesData)(nil)

type archiveFilesData struct{ dataBase }

func newArchiveFilesData() datasource.DataSource { return &archiveFilesData{} }

type archiveFilesModel struct {
	ID        types.String     `tfsdk:"id"`
	Archive   types.String     `tfsdk:"archive"`
	ArchiveID types.String     `tfsdk:"archive_id"`
	Folder    types.String     `tfsdk:"folder"`
	Recursive types.Bool       `tfsdk:"recursive"`
	Files     []archiveFileRow `tfsdk:"files"`
}

type archiveFileRow struct {
	ID          types.String `tfsdk:"id"`
	Name        types.String `tfsdk:"name"`
	Path        types.String `tfsdk:"path"`
	IsDirectory types.Bool   `tfsdk:"is_directory"`
	SizeBytes   types.Int64  `tfsdk:"size_bytes"`
	ContentType types.String `tfsdk:"content_type"`
	LastUpdated types.String `tfsdk:"last_updated"`
}

func (d *archiveFilesData) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_archive_files"
}

func (d *archiveFilesData) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = ds.Schema{
		Description: "Lists the files and folders in one folder of an archive (Tools > Archives), or every file under it.",
		Attributes: map[string]ds.Attribute{
			"id":      ds.StringAttribute{Description: "`<archive_id>:<folder>`.", Computed: true},
			"archive": ds.StringAttribute{Description: "The archive, by name. Set this or `archive_id`.", Optional: true, Computed: true},
			"archive_id": ds.StringAttribute{Description: "The archive, by ID. Set this or `archive`.", Optional: true, Computed: true,
				Validators: []validator.String{stringvalidator.RegexMatches(digits, "must be an archive ID (digits)")}},
			"folder": ds.StringAttribute{Description: "The folder, for example `reports/2026`. Leave it out for the top of the archive. " +
				"A folder that doesn't exist has no files.", Optional: true},
			"recursive": ds.BoolAttribute{Description: "List every file under the folder, in all its subfolders, instead of what is in the folder itself.", Optional: true},
			"files": ds.ListNestedAttribute{
				Description: "The files and folders, sorted by path.", Computed: true,
				NestedObject: ds.NestedAttributeObject{Attributes: map[string]ds.Attribute{
					"id":           ds.StringAttribute{Description: "The file's ID (`mtncloud_archive_file_link.file_id`).", Computed: true},
					"name":         ds.StringAttribute{Description: "Its name.", Computed: true},
					"path":         ds.StringAttribute{Description: "Its whole path in the archive.", Computed: true},
					"is_directory": ds.BoolAttribute{Description: "Whether it is a folder.", Computed: true},
					"size_bytes":   ds.Int64Attribute{Description: "Its size in bytes.", Computed: true},
					"content_type": ds.StringAttribute{Description: "Its content type.", Computed: true},
					"last_updated": ds.StringAttribute{Description: "When it was last written (RFC 3339).", Computed: true},
				}},
			},
		},
	}
}

func (d *archiveFilesData) ConfigValidators(context.Context) []datasource.ConfigValidator {
	return []datasource.ConfigValidator{datasourcevalidator.ExactlyOneOf(path.MatchRoot("archive"), path.MatchRoot("archive_id"))}
}

func (d *archiveFilesData) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	if !d.ready(&resp.Diagnostics) {
		return
	}
	var m archiveFilesModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if !m.ArchiveID.IsNull() {
		id, _ := strconv.ParseInt(m.ArchiveID.ValueString(), 10, 64)
		a, err := d.client.GetArchiveBucket(ctx, id)
		if err != nil {
			resp.Diagnostics.AddAttributeError(path.Root("archive_id"), "Unable to read the archive", err.Error())
			return
		}
		m.Archive = types.StringValue(a.Name)
	} else {
		a, err := d.client.FindArchiveBucket(ctx, m.Archive.ValueString())
		switch {
		case err != nil:
			resp.Diagnostics.AddAttributeError(path.Root("archive"), "Unable to look up the archive", err.Error())
			return
		case a == nil:
			resp.Diagnostics.AddAttributeError(path.Root("archive"), "No such archive", fmt.Sprintf("Your tenant has no archive named %q.", m.Archive.ValueString()))
			return
		}
		m.ArchiveID = types.StringValue(strconv.FormatInt(a.ID, 10))
	}
	folder := strings.Trim(m.Folder.ValueString(), "/")
	files, err := d.client.ListArchiveFiles(ctx, m.Archive.ValueString(), folder, m.Recursive.ValueBool())
	if err != nil {
		resp.Diagnostics.AddError("Unable to list the archive's files", err.Error())
		return
	}
	sort.Slice(files, func(i, j int) bool { return files[i].FilePath < files[j].FilePath })
	m.Files = make([]archiveFileRow, 0, len(files))
	for _, f := range files {
		m.Files = append(m.Files, archiveFileRow{
			ID: types.StringValue(strconv.FormatInt(f.ID, 10)), Name: types.StringValue(f.Name), Path: types.StringValue(f.FilePath),
			IsDirectory: types.BoolValue(f.IsDirectory), SizeBytes: types.Int64Value(f.RawSize),
			ContentType: types.StringValue(f.ContentType), LastUpdated: types.StringValue(f.LastUpdated),
		})
	}
	m.ID = types.StringValue(m.ArchiveID.ValueString() + ":" + folder)
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}
