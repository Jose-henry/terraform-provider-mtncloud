package lookup

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/mtn/terraform-provider-mtncloud/internal/client"
)

func newImage() datasource.DataSource { return &imageDataSource{} }

// imageDataSource is mtncloud_image: one OS image you can provision, by name or code.
type imageDataSource struct{ base }

type oneImageModel struct {
	Name     types.String `tfsdk:"name"`
	ID       types.Int64  `tfsdk:"id"`
	Code     types.String `tfsdk:"code"`
	FullName types.String `tfsdk:"full_name"`
	LayoutID types.Int64  `tfsdk:"layout_id"`
}

func (d *imageDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_image"
}

func (d *imageDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Looks up one OS image you can provision, by its name or its code. To list them, use `mtncloud_images`.",
		Attributes: map[string]schema.Attribute{
			"name": schema.StringAttribute{
				Required:    true,
				Description: "Image name, for example `MTN Ubuntu Server 24.04.3LTS` (case ignored), or its code, for example `MTN-U24.04LTS`.",
			},
			"id":        schema.Int64Attribute{Computed: true, Description: "Image (instance type) ID."},
			"code":      schema.StringAttribute{Computed: true, Description: "Image code, for example `MTN-U24.04LTS`."},
			"full_name": schema.StringAttribute{Computed: true, Description: "Display name, for example `MTN Ubuntu Server 24.04.3LTS`."},
			"layout_id": schema.Int64Attribute{Computed: true, Description: "Layout ID used to provision the image."},
		},
	}
}

func (d *imageDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	if !d.ready(&resp.Diagnostics) {
		return
	}
	var m oneImageModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	img, err := d.client.ImageByName(ctx, m.Name.ValueString())
	if client.IsNoMatch(err) {
		resp.Diagnostics.AddAttributeError(path.Root("name"), "No such image", err.Error())
		return
	}
	if err != nil {
		apiError(&resp.Diagnostics, "images", err)
		return
	}
	m.ID, m.Code, m.FullName, m.LayoutID = types.Int64Value(img.ID), types.StringValue(img.Code), types.StringValue(img.Name), types.Int64Value(img.LayoutID)
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}
