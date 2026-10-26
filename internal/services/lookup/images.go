package lookup

import (
	"cmp"
	"context"
	"slices"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/mtn/terraform-provider-mtncloud/internal/validators"
)

func newImages() datasource.DataSource { return &imagesDataSource{} }

type imagesDataSource struct{ base }

type imagesModel struct {
	NameRegex types.String `tfsdk:"name_regex"`
	Images    []imageModel `tfsdk:"images"`
}

type imageModel struct {
	ID       types.Int64  `tfsdk:"id"`
	Code     types.String `tfsdk:"code"`
	Name     types.String `tfsdk:"name"`
	LayoutID types.Int64  `tfsdk:"layout_id"`
}

func (d *imagesDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_images"
}

func (d *imagesDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Lists the operating system images you can create instances from, for example `MTN-U24.04LTS` (Ubuntu 24.04). " +
			"Applications such as Kubernetes, databases and VPN are ordered from the catalog instead: see `mtncloud_catalog_items`.",
		Attributes: map[string]schema.Attribute{
			"name_regex": schema.StringAttribute{
				Optional:    true,
				Description: "Only return images whose code or name matches this regular expression (RE2 syntax), for example `Ubuntu|Debian`.",
				Validators:  []validator.String{validators.Regex()},
			},
			"images": schema.ListNestedAttribute{
				Computed:    true,
				Description: "The images, sorted by code.",
				NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
					"id":        schema.Int64Attribute{Computed: true, Description: "Image (instance type) ID."},
					"code":      schema.StringAttribute{Computed: true, Description: "Image code, for example `MTN-U24.04LTS`. Use this to pick an image."},
					"name":      schema.StringAttribute{Computed: true, Description: "Display name, for example `MTN Ubuntu Server 24.04.3LTS`."},
					"layout_id": schema.Int64Attribute{Computed: true, Description: "OpenStack layout ID."},
				}},
			},
		},
	}
}

func (d *imagesDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	if !d.ready(&resp.Diagnostics) {
		return
	}
	var m imagesModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	match, diags := matcher(m.NameRegex)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	all, err := d.client.InstanceTypes(ctx)
	if err != nil {
		apiError(&resp.Diagnostics, "images", err)
		return
	}
	m.Images = []imageModel{}
	for _, t := range all {
		// Only operating system images that can run on OpenStack (D-011).
		if t.Category != "os" || t.LayoutID == 0 || (!match(t.Code) && !match(t.Name)) {
			continue
		}
		m.Images = append(m.Images, imageModel{
			ID: types.Int64Value(t.ID), Code: types.StringValue(t.Code),
			Name: types.StringValue(t.Name), LayoutID: types.Int64Value(t.LayoutID),
		})
	}
	slices.SortFunc(m.Images, func(a, b imageModel) int {
		return cmp.Or(cmp.Compare(a.Code.ValueString(), b.Code.ValueString()), cmp.Compare(a.ID.ValueInt64(), b.ID.ValueInt64()))
	})
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}
