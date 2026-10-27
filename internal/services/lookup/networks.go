package lookup

import (
	"cmp"
	"context"
	"slices"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/mtn/terraform-provider-mtncloud/internal/normalize"
)

// newNetworks replaces the inherited mtncloud_networks: it keeps "ids", and lists each network
// with its project, group and cloud.
func newNetworks() datasource.DataSource { return &networksDataSource{} }

type networksDataSource struct{ base }

type networksModel struct {
	NameRegex types.String      `tfsdk:"name_regex"`
	Project   types.String      `tfsdk:"project"`
	IDs       []types.Int64     `tfsdk:"ids"`
	Networks  []networkRowModel `tfsdk:"networks"`
}

type networkRowModel struct {
	ID      types.Int64  `tfsdk:"id"`
	Name    types.String `tfsdk:"name"`
	Project types.String `tfsdk:"project"`
	Group   types.String `tfsdk:"group"`
	Cloud   types.String `tfsdk:"cloud"`
	CIDR    types.String `tfsdk:"cidr"`
	Gateway types.String `tfsdk:"gateway"`
	Status  types.String `tfsdk:"status"`
}

func (d *networksDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_networks"
}

func (d *networksDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Lists your networks, including those made in the console, with their project, group and cloud. " +
			"For one network's full settings, use `mtncloud_network`.",
		Attributes: map[string]schema.Attribute{
			"name_regex": nameRegexAttribute("networks"),
			"project": schema.StringAttribute{
				Optional:    true,
				Description: "Only list networks in this project (for example `web-prod`; the full platform name also works).",
			},
			"ids": schema.ListAttribute{
				Computed: true, ElementType: types.Int64Type,
				Description: "IDs of the networks listed, in the same order as `networks`.",
			},
			"networks": schema.ListNestedAttribute{
				Computed:    true,
				Description: "The networks, sorted by name, then ID.",
				NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
					"id":      schema.Int64Attribute{Computed: true, Description: "Network ID."},
					"name":    schema.StringAttribute{Computed: true, Description: "Network name."},
					"project": schema.StringAttribute{Computed: true, Description: "Project, without the tenant suffix; empty for a network in no project (an external network)."},
					"group":   schema.StringAttribute{Computed: true, Description: "Group the network belongs to."},
					"cloud":   schema.StringAttribute{Computed: true, Description: "Cloud the network is in."},
					"cidr":    schema.StringAttribute{Computed: true, Description: "IPv4 CIDR, or empty."},
					"gateway": schema.StringAttribute{Computed: true, Description: "IPv4 gateway, or empty."},
					"status":  schema.StringAttribute{Computed: true, Description: "Status, for example `ok`."},
				}},
			},
		},
	}
}

func (d *networksDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	if !d.ready(&resp.Diagnostics) {
		return
	}
	var m networksModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	match, diags := matcher(m.NameRegex)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	who, err := d.client.WhoAmI(ctx)
	if err != nil {
		apiError(&resp.Diagnostics, "networks", err)
		return
	}
	var projectID int64
	if !m.Project.IsNull() {
		p, err := d.client.ProjectByName(ctx, m.Project.ValueString())
		if err != nil {
			projectError(&resp.Diagnostics, err)
			return
		}
		projectID = p.ID
	}
	all, err := d.client.ListNetworks(ctx)
	if err != nil {
		apiError(&resp.Diagnostics, "networks", err)
		return
	}
	m.Networks = []networkRowModel{}
	for _, n := range all {
		if !match(n.Name) || (projectID != 0 && n.ProjectID != projectID) {
			continue
		}
		project := ""
		if n.ProjectName != "" {
			project = normalize.ProjectShortName(n.ProjectName, who.AccountName)
		}
		m.Networks = append(m.Networks, networkRowModel{
			ID: types.Int64Value(n.ID), Name: types.StringValue(n.Name), Project: types.StringValue(project),
			Group: types.StringValue(n.GroupName), Cloud: types.StringValue(n.CloudName),
			CIDR: types.StringValue(n.CIDR), Gateway: types.StringValue(n.Gateway), Status: types.StringValue(n.Status),
		})
	}
	slices.SortFunc(m.Networks, func(a, b networkRowModel) int {
		return cmp.Or(cmp.Compare(a.Name.ValueString(), b.Name.ValueString()), cmp.Compare(a.ID.ValueInt64(), b.ID.ValueInt64()))
	})
	m.IDs = make([]types.Int64, len(m.Networks))
	for i, n := range m.Networks {
		m.IDs[i] = n.ID
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}
