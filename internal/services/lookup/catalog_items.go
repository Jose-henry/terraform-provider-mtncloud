package lookup

import (
	"cmp"
	"context"
	"slices"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

func newCatalogItems() datasource.DataSource { return &catalogItemsDataSource{} }

type catalogItemsDataSource struct{ base }

type catalogItemsModel struct {
	NameRegex types.String       `tfsdk:"name_regex"`
	Type      types.String       `tfsdk:"type"`
	Items     []catalogItemModel `tfsdk:"items"`
}

type catalogItemModel struct {
	ID          types.Int64  `tfsdk:"id"`
	Name        types.String `tfsdk:"name"`
	Description types.String `tfsdk:"description"`
	Type        types.String `tfsdk:"type"`
	Featured    types.Bool   `tfsdk:"featured"`
}

func (d *catalogItemsDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_catalog_items"
}

func (d *catalogItemsDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Lists the services you can order from the MTN Cloud catalog: projects, Kubernetes, managed databases, VPN, firewall, " +
			"MTN Object Storage (MOS) and more. Pick an item by `name`.",
		Attributes: map[string]schema.Attribute{
			"name_regex": nameRegexAttribute("catalog items"),
			"type": schema.StringAttribute{
				Optional:    true,
				Description: "Only return items of this type: `instance`, `blueprint` or `workflow`.",
				Validators:  []validator.String{stringvalidator.OneOf("instance", "blueprint", "workflow")},
			},
			"items": schema.ListNestedAttribute{
				Computed:    true,
				Description: "The catalog items, sorted by name.",
				NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
					"id":          schema.Int64Attribute{Computed: true, Description: "Catalog item ID."},
					"name":        schema.StringAttribute{Computed: true, Description: "Name shown in the catalog."},
					"description": schema.StringAttribute{Computed: true, Description: "Description shown in the catalog."},
					"type":        schema.StringAttribute{Computed: true, Description: "`instance`, `blueprint` or `workflow`."},
					"featured":    schema.BoolAttribute{Computed: true, Description: "Whether the catalog features this item."},
				}},
			},
		},
	}
}

func (d *catalogItemsDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	if !d.ready(&resp.Diagnostics) {
		return
	}
	var m catalogItemsModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	match, diags := matcher(m.NameRegex)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	items, err := d.client.CatalogItems(ctx)
	if err != nil {
		apiError(&resp.Diagnostics, "catalog items", err)
		return
	}
	m.Items = []catalogItemModel{}
	for _, it := range items {
		if !match(it.Name) || (!m.Type.IsNull() && it.Type != m.Type.ValueString()) {
			continue
		}
		m.Items = append(m.Items, catalogItemModel{
			ID: types.Int64Value(it.ID), Name: types.StringValue(it.Name), Description: types.StringValue(it.Description),
			Type: types.StringValue(it.Type), Featured: types.BoolValue(it.Featured),
		})
	}
	slices.SortFunc(m.Items, func(a, b catalogItemModel) int {
		return cmp.Or(cmp.Compare(a.Name.ValueString(), b.Name.ValueString()), cmp.Compare(a.ID.ValueInt64(), b.ID.ValueInt64()))
	})
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}
