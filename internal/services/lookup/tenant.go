package lookup

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

func newTenant() datasource.DataSource { return &tenantDataSource{} }

// tenantDataSource is mtncloud_tenant: the tenant the provider is logged in to, so its ID never
// has to be copied from the console (identity sources, users).
type tenantDataSource struct{ base }

type tenantModel struct {
	ID     types.Int64  `tfsdk:"id"`
	Name   types.String `tfsdk:"name"`
	Master types.Bool   `tfsdk:"master"`
}

func (d *tenantDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_tenant"
}

func (d *tenantDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "The tenant the provider is logged in to: its ID and name. Everything this provider makes is made " +
			"in this tenant.",
		Attributes: map[string]schema.Attribute{
			"id":     schema.Int64Attribute{Computed: true, Description: "The tenant's ID."},
			"name":   schema.StringAttribute{Computed: true, Description: "The tenant's name. Projects are named `<project>-<name>`."},
			"master": schema.BoolAttribute{Computed: true, Description: "Whether this is the master tenant."},
		},
	}
}

func (d *tenantDataSource) Read(ctx context.Context, _ datasource.ReadRequest, resp *datasource.ReadResponse) {
	if !d.ready(&resp.Diagnostics) {
		return
	}
	who, err := d.client.WhoAmI(ctx)
	if err != nil {
		apiError(&resp.Diagnostics, "your tenant", err)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &tenantModel{
		ID: types.Int64Value(who.AccountID), Name: types.StringValue(who.AccountName), Master: types.BoolValue(who.IsMaster),
	})...)
}
