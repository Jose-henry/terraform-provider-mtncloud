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

func newFloatingIPs() datasource.DataSource { return &floatingIPsDataSource{} }

// floatingIPsDataSource is mtncloud_floating_ips: the public IPs on your instances. The
// floating IP endpoint itself (/api/networks/floating-ips) refuses customer logins (403,
// 2026-10-27), so this reads the instances, which a customer can.
type floatingIPsDataSource struct{ base }

type floatingIPsModel struct {
	Project     types.String      `tfsdk:"project"`
	FloatingIPs []floatingIPModel `tfsdk:"floating_ips"`
}

type floatingIPModel struct {
	IP         types.String `tfsdk:"ip"`
	InstanceID types.Int64  `tfsdk:"instance_id"`
	Instance   types.String `tfsdk:"instance"`
	Project    types.String `tfsdk:"project"`
	PrivateIP  types.String `tfsdk:"private_ip"`
}

func (d *floatingIPsDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_floating_ips"
}

func (d *floatingIPsDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Lists the floating (public) IPs attached to your instances, with the instance each one is on. Give an " +
			"instance a floating IP with `floating_ip_pool` on `mtncloud_instance`. Floating IPs not attached to an instance " +
			"aren't listed.",
		Attributes: map[string]schema.Attribute{
			"project": schema.StringAttribute{
				Optional:    true,
				Description: "Only list IPs on instances in this project (for example `web-prod`; the full platform name also works).",
			},
			"floating_ips": schema.ListNestedAttribute{
				Computed:    true,
				Description: "The floating IPs, sorted by address.",
				NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
					"ip":          schema.StringAttribute{Computed: true, Description: "The public address."},
					"instance_id": schema.Int64Attribute{Computed: true, Description: "ID of the instance it is on."},
					"instance":    schema.StringAttribute{Computed: true, Description: "Name of the instance it is on."},
					"project":     schema.StringAttribute{Computed: true, Description: "The instance's project, without the tenant suffix."},
					"private_ip":  schema.StringAttribute{Computed: true, Description: "The instance's private address it forwards to."},
				}},
			},
		},
	}
}

func (d *floatingIPsDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	if !d.ready(&resp.Diagnostics) {
		return
	}
	var m floatingIPsModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	who, err := d.client.WhoAmI(ctx)
	if err != nil {
		apiError(&resp.Diagnostics, "floating IPs", err)
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
	projects, err := d.client.Projects(ctx)
	if err != nil {
		apiError(&resp.Diagnostics, "projects", err)
		return
	}
	names := map[int64]string{}
	for _, p := range projects {
		names[p.ID] = normalize.ProjectShortName(p.Name, who.AccountName)
	}
	instances, err := d.client.ListInstances(ctx)
	if err != nil {
		apiError(&resp.Diagnostics, "instances", err)
		return
	}
	m.FloatingIPs = []floatingIPModel{}
	for _, in := range instances {
		if in.PublicIP == "" || (projectID != 0 && in.ProjectID != projectID) {
			continue
		}
		m.FloatingIPs = append(m.FloatingIPs, floatingIPModel{
			IP: types.StringValue(in.PublicIP), InstanceID: types.Int64Value(in.ID), Instance: types.StringValue(in.Name),
			Project: types.StringValue(names[in.ProjectID]), PrivateIP: types.StringValue(in.PrivateIP),
		})
	}
	slices.SortFunc(m.FloatingIPs, func(a, b floatingIPModel) int {
		return cmp.Or(cmp.Compare(a.IP.ValueString(), b.IP.ValueString()), cmp.Compare(a.InstanceID.ValueInt64(), b.InstanceID.ValueInt64()))
	})
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}
