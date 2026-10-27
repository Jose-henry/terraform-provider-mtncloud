package lookup

import (
	"cmp"
	"context"
	"slices"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/mtn/terraform-provider-mtncloud/internal/client"
)

func newRouters() datasource.DataSource { return &routersDataSource{} }

type routersDataSource struct{ base }

type routersModel struct {
	NameRegex types.String  `tfsdk:"name_regex"`
	Routers   []routerModel `tfsdk:"routers"`
}

type routerModel struct {
	ID              types.Int64   `tfsdk:"id"`
	Name            types.String  `tfsdk:"name"`
	Description     types.String  `tfsdk:"description"`
	Status          types.String  `tfsdk:"status"`
	Enabled         types.Bool    `tfsdk:"enabled"`
	ExternalNetwork types.String  `tfsdk:"external_network"`
	ExternalIP      types.String  `tfsdk:"external_ip"`
	Networks        []types.Int64 `tfsdk:"networks"`
}

func (d *routersDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_routers"
}

func (d *routersDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Lists your MTN Routers, including those made in the console. Use a router's `id` to import it, or its " +
			"`external_ip` and `networks` in other configuration.",
		Attributes: map[string]schema.Attribute{
			"name_regex": nameRegexAttribute("routers"),
			"routers": schema.ListNestedAttribute{
				Computed:    true,
				Description: "The routers, sorted by name, then ID.",
				NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
					"id":          schema.Int64Attribute{Computed: true, Description: "Router ID."},
					"name":        schema.StringAttribute{Computed: true, Description: "Router name."},
					"description": schema.StringAttribute{Computed: true, Description: "Router description, or empty."},
					"status":      schema.StringAttribute{Computed: true, Description: "Status, for example `ok`."},
					"enabled":     schema.BoolAttribute{Computed: true, Description: "Whether the router is enabled."},
					"external_network": schema.StringAttribute{
						Computed: true, Description: "The external network that is its gateway, or empty without one.",
					},
					"external_ip": schema.StringAttribute{Computed: true, Description: "Its public IP on the external network, or empty."},
					"networks": schema.ListAttribute{
						Computed: true, ElementType: types.Int64Type, Description: "IDs of the MTN Networks it connects.",
					},
				}},
			},
		},
	}
}

func (d *routersDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	if !d.ready(&resp.Diagnostics) {
		return
	}
	var m routersModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	match, diags := matcher(m.NameRegex)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	routers, err := d.client.ListRouters(ctx)
	if err != nil {
		apiError(&resp.Diagnostics, "routers", err)
		return
	}
	m.Routers = []routerModel{}
	for _, r := range routers {
		if !match(r.Name) {
			continue
		}
		m.Routers = append(m.Routers, routerFields(r))
	}
	slices.SortFunc(m.Routers, func(a, b routerModel) int {
		return cmp.Or(cmp.Compare(a.Name.ValueString(), b.Name.ValueString()), cmp.Compare(a.ID.ValueInt64(), b.ID.ValueInt64()))
	})
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

func routerFields(r *client.Router) routerModel {
	nets := make([]types.Int64, len(r.NetworkIDs))
	for i, id := range r.NetworkIDs {
		nets[i] = types.Int64Value(id)
	}
	return routerModel{
		ID: types.Int64Value(r.ID), Name: types.StringValue(r.Name), Description: types.StringValue(r.Description),
		Status: types.StringValue(r.Status), Enabled: types.BoolValue(r.Enabled),
		ExternalNetwork: types.StringValue(r.ExternalNetworkName), ExternalIP: types.StringValue(r.ExternalIP),
		Networks: nets,
	}
}
