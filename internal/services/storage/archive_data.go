package storage

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strconv"

	"github.com/hashicorp/terraform-plugin-framework-validators/datasourcevalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	ds "github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/mtn/terraform-provider-mtncloud/internal/client"
)

var digits = regexp.MustCompile(`^[0-9]+$`)

// archiveRow is one archive, as both archive data sources show it.
type archiveRow struct {
	ID          types.String `tfsdk:"id"`
	Name        types.String `tfsdk:"name"`
	Description types.String `tfsdk:"description"`
	MOSBucketID types.String `tfsdk:"mos_bucket_id"`
	ConsoleName types.String `tfsdk:"mos_bucket_console_name"`
	PublicURL   types.Bool   `tfsdk:"public_url"`
	Visibility  types.String `tfsdk:"visibility"`
	Owner       types.String `tfsdk:"owner"`
	FileCount   types.Int64  `tfsdk:"file_count"`
	SizeBytes   types.Int64  `tfsdk:"size_bytes"`
}

func archiveRowOf(a *client.ArchiveBucket) archiveRow {
	return archiveRow{
		ID: types.StringValue(strconv.FormatInt(a.ID, 10)), Name: types.StringValue(a.Name), Description: types.StringValue(a.Description),
		MOSBucketID: types.StringValue(strconv.FormatInt(a.StorageBucketID, 10)), ConsoleName: types.StringValue(a.StorageBucket),
		PublicURL: types.BoolValue(a.IsPublic), Visibility: types.StringValue(a.Visibility), Owner: types.StringValue(a.Owner),
		FileCount: types.Int64Value(a.FileCount), SizeBytes: types.Int64Value(a.RawSize),
	}
}

// archiveRowAttributes are the attributes of one archive; id and name are left to the caller.
func archiveRowAttributes() map[string]ds.Attribute {
	return map[string]ds.Attribute{
		"description":             ds.StringAttribute{Description: "Its description.", Computed: true},
		"mos_bucket_id":           ds.StringAttribute{Description: "The console record ID of the MOS bucket its files are kept in (an `mtncloud_mos_bucket` ID).", Computed: true},
		"mos_bucket_console_name": ds.StringAttribute{Description: "That bucket's name in the console.", Computed: true},
		"public_url":              ds.BoolAttribute{Description: "Whether anyone can download its files without logging in.", Computed: true},
		"visibility":              ds.StringAttribute{Description: "`private` (its own tenant) or `public` (every tenant).", Computed: true},
		"owner":                   ds.StringAttribute{Description: "The tenant that owns it.", Computed: true},
		"file_count":              ds.Int64Attribute{Description: "How many files it holds.", Computed: true},
		"size_bytes":              ds.Int64Attribute{Description: "The total size of its files, in bytes.", Computed: true},
	}
}

// --- one archive

var _ datasource.DataSourceWithConfigValidators = (*archiveData)(nil)

type archiveData struct{ dataBase }

func newArchiveData() datasource.DataSource { return &archiveData{} }

func (d *archiveData) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_archive_bucket"
}

func (d *archiveData) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	attrs := archiveRowAttributes()
	attrs["id"] = ds.StringAttribute{Description: "The archive's ID. Set this or `name`.", Optional: true, Computed: true,
		Validators: []validator.String{stringvalidator.RegexMatches(digits, "must be an archive ID (digits)")}}
	attrs["name"] = ds.StringAttribute{Description: "The archive's name. Set this or `id`.", Optional: true, Computed: true}
	resp.Schema = ds.Schema{Description: "Reads one archive (Tools > Archives), by ID or name.", Attributes: attrs}
}

func (d *archiveData) ConfigValidators(context.Context) []datasource.ConfigValidator {
	return []datasource.ConfigValidator{datasourcevalidator.ExactlyOneOf(path.MatchRoot("id"), path.MatchRoot("name"))}
}

func (d *archiveData) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	if !d.ready(&resp.Diagnostics) {
		return
	}
	var cfg archiveRow
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var a *client.ArchiveBucket
	var err error
	if !cfg.ID.IsNull() {
		id, _ := strconv.ParseInt(cfg.ID.ValueString(), 10, 64)
		if a, err = d.client.GetArchiveBucket(ctx, id); client.IsNotFound(err) {
			resp.Diagnostics.AddAttributeError(path.Root("id"), "No such archive", fmt.Sprintf("Your tenant has no archive with ID %d.", id))
			return
		}
	} else if a, err = d.client.FindArchiveBucket(ctx, cfg.Name.ValueString()); err == nil && a == nil {
		resp.Diagnostics.AddAttributeError(path.Root("name"), "No such archive", fmt.Sprintf("Your tenant has no archive named %q.", cfg.Name.ValueString()))
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Unable to read the archive", err.Error())
		return
	}
	row := archiveRowOf(a)
	resp.Diagnostics.Append(resp.State.Set(ctx, &row)...)
}

// --- every archive

type archivesData struct{ dataBase }

type archivesModel struct {
	ID       types.String `tfsdk:"id"`
	Archives []archiveRow `tfsdk:"archives"`
}

func newArchivesData() datasource.DataSource { return &archivesData{} }

func (d *archivesData) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_archive_buckets"
}

func (d *archivesData) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	row := archiveRowAttributes()
	row["id"] = ds.StringAttribute{Description: "The archive's ID.", Computed: true}
	row["name"] = ds.StringAttribute{Description: "The archive's name.", Computed: true}
	resp.Schema = ds.Schema{
		Description: "Lists the archives (Tools > Archives) your tenant can see, sorted by name.",
		Attributes: map[string]ds.Attribute{
			"id":       ds.StringAttribute{Description: "Always `archive-buckets`.", Computed: true},
			"archives": ds.ListNestedAttribute{Description: "The archives.", Computed: true, NestedObject: ds.NestedAttributeObject{Attributes: row}},
		},
	}
}

func (d *archivesData) Read(ctx context.Context, _ datasource.ReadRequest, resp *datasource.ReadResponse) {
	if !d.ready(&resp.Diagnostics) {
		return
	}
	all, err := d.client.ListArchiveBuckets(ctx)
	if err != nil {
		resp.Diagnostics.AddError("Unable to list archives", err.Error())
		return
	}
	sort.Slice(all, func(i, j int) bool { return all[i].Name < all[j].Name })
	m := archivesModel{ID: types.StringValue("archive-buckets"), Archives: make([]archiveRow, 0, len(all))}
	for _, a := range all {
		m.Archives = append(m.Archives, archiveRowOf(a))
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}
