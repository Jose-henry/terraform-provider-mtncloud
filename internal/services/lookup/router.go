package lookup

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/datasourcevalidator"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/mtn/terraform-provider-mtncloud/internal/client"
)

func newRouter() datasource.DataSource { return &routerDataSource{} }

// routerDataSource is mtncloud_router: one MTN Router, with the fields mtncloud_routers lists.
type routerDataSource struct{ base }

func (d *routerDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_router"
}

func (d *routerDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Looks up one MTN Router by ID or by name, including one made in the console. A name must match exactly one " +
			"router. To list them, use `mtncloud_routers`.",
		Attributes: map[string]schema.Attribute{
			// Optional+Computed: give id or name, and the other is filled in.
			"id":          schema.Int64Attribute{Optional: true, Computed: true, Description: "Router ID. Give this or `name`."},
			"name":        schema.StringAttribute{Optional: true, Computed: true, Description: "Router name, matched exactly. Give this or `id`."},
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
		},
	}
}

func (d *routerDataSource) ConfigValidators(context.Context) []datasource.ConfigValidator {
	return []datasource.ConfigValidator{datasourcevalidator.ExactlyOneOf(path.MatchRoot("id"), path.MatchRoot("name"))}
}

func (d *routerDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	if !d.ready(&resp.Diagnostics) {
		return
	}
	var m routerModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var r *client.Router
	if !m.ID.IsNull() {
		var err error
		r, err = d.client.GetRouter(ctx, m.ID.ValueInt64())
		if client.IsNotFound(err) {
			resp.Diagnostics.AddAttributeError(path.Root("id"), "No such router", fmt.Sprintf("There is no router %d you can see.", m.ID.ValueInt64()))
			return
		}
		if err != nil {
			apiError(&resp.Diagnostics, "the router", err)
			return
		}
	} else {
		found, err := d.client.FindRouters(ctx, m.Name.ValueString())
		if err != nil {
			apiError(&resp.Diagnostics, "routers", err)
			return
		}
		switch len(found) {
		case 1:
			r = found[0]
		case 0:
			resp.Diagnostics.AddAttributeError(path.Root("name"), "No such router",
				fmt.Sprintf("No router is named %q. Names are matched exactly; mtncloud_routers lists them.", m.Name.ValueString()))
			return
		default:
			ids := make([]string, len(found))
			for i, x := range found {
				ids[i] = strconv.FormatInt(x.ID, 10)
			}
			resp.Diagnostics.AddAttributeError(path.Root("name"), "Several routers match",
				fmt.Sprintf("%d routers are named %q (IDs %s). Look one up by id.", len(found), m.Name.ValueString(), strings.Join(ids, ", ")))
			return
		}
	}
	m = routerFields(r)
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}
