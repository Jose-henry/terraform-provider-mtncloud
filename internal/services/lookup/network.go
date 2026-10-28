package lookup

import (
	"context"
	"fmt"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/datasourcevalidator"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/mtn/terraform-provider-mtncloud/internal/client"
	"github.com/mtn/terraform-provider-mtncloud/internal/normalize"
)

// newNetwork replaces the inherited mtncloud_network data source: same type name and attributes,
// plus the network's project, group and cloud.
func newNetwork() datasource.DataSource { return &networkDataSource{} }

type networkDataSource struct{ base }

type networkModel struct {
	ID           types.Int64    `tfsdk:"id"`
	Name         types.String   `tfsdk:"name"`
	Project      types.String   `tfsdk:"project"`
	Group        types.String   `tfsdk:"group"`
	Cloud        types.String   `tfsdk:"cloud"`
	DisplayName  types.String   `tfsdk:"display_name"`
	Description  types.String   `tfsdk:"description"`
	Labels       []types.String `tfsdk:"labels"`
	Active       types.Bool     `tfsdk:"active"`
	Visibility   types.String   `tfsdk:"visibility"`
	Status       types.String   `tfsdk:"status"`
	CIDR         types.String   `tfsdk:"cidr"`
	Gateway      types.String   `tfsdk:"gateway"`
	DNSPrimary   types.String   `tfsdk:"dns_primary"`
	DNSSecondary types.String   `tfsdk:"dns_secondary"`
	DHCP         types.Bool     `tfsdk:"dhcp"`
	SubnetID     types.Int64    `tfsdk:"subnet_id"`
}

func (d *networkDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_network"
}

func (d *networkDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	computed := func(desc string) schema.StringAttribute {
		return schema.StringAttribute{Computed: true, Description: desc}
	}
	resp.Schema = schema.Schema{
		Description: "Looks up one MTN Network by ID or by name, including one made in the console, with its project, group and cloud. " +
			"A name must match exactly one network; add `project` when the same name is used in several projects.",
		Attributes: map[string]schema.Attribute{
			// Optional+Computed: give id or name, and the other is filled in.
			"id":   schema.Int64Attribute{Optional: true, Computed: true, Description: "Network ID. Give this or `name`."},
			"name": schema.StringAttribute{Optional: true, Computed: true, Description: "Network name, matched exactly. Give this or `id`."},
			// Optional+Computed: narrows a name lookup, and is filled in from the network either way.
			"project": schema.StringAttribute{
				Optional: true, Computed: true,
				Description: "Project name (for example `web-prod`), to tell apart networks with the same name. The full platform " +
					"name, with the tenant suffix, also works. Always set to the network's project, without the suffix.",
			},
			"group":         computed("Group the network belongs to."),
			"cloud":         computed("Cloud the network is in, for example `MTNNG_CLOUD_AZ_1`."),
			"display_name":  computed("Display name, or empty."),
			"description":   computed("Description, or empty."),
			"labels":        schema.ListAttribute{Computed: true, ElementType: types.StringType, Description: "Labels."},
			"active":        schema.BoolAttribute{Computed: true, Description: "Whether the network is active."},
			"visibility":    computed("`private` or `public`."),
			"status":        computed("Status, for example `ok`."),
			"cidr":          computed("IPv4 CIDR, for example `10.20.1.0/24`."),
			"gateway":       computed("IPv4 gateway."),
			"dns_primary":   computed("Primary DNS server."),
			"dns_secondary": computed("Secondary DNS server."),
			"dhcp":          schema.BoolAttribute{Computed: true, Description: "Whether DHCP is on."},
			"subnet_id":     schema.Int64Attribute{Computed: true, Description: "ID of the network's subnet (what an MTN Router connects); 0 while it is created."},
		},
	}
}

func (d *networkDataSource) ConfigValidators(context.Context) []datasource.ConfigValidator {
	return []datasource.ConfigValidator{datasourcevalidator.ExactlyOneOf(path.MatchRoot("id"), path.MatchRoot("name"))}
}

func (d *networkDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	if !d.ready(&resp.Diagnostics) {
		return
	}
	var m networkModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	who, err := d.client.WhoAmI(ctx)
	if err != nil {
		apiError(&resp.Diagnostics, "network", err)
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
	var n *client.Network
	if !m.ID.IsNull() {
		n, err = d.client.GetNetwork(ctx, m.ID.ValueInt64())
		if client.IsNotFound(err) {
			resp.Diagnostics.AddAttributeError(path.Root("id"), "No such network", fmt.Sprintf("There is no network %d you can see.", m.ID.ValueInt64()))
			return
		}
		if err == nil && projectID != 0 && n.ProjectID != 0 && n.ProjectID != projectID {
			resp.Diagnostics.AddAttributeError(path.Root("project"), "Network in another project",
				fmt.Sprintf("Network %d isn't in project %s. Remove project, or fix it.", n.ID, m.Project.ValueString()))
			return
		}
	} else {
		var all []*client.Network
		all, err = d.client.FindNetworksNamed(ctx, m.Name.ValueString())
		if err == nil {
			n = oneNetwork(all, path.Root("name"), m.Name.ValueString(), m.Project, projectID, &resp.Diagnostics)
			if n == nil {
				return
			}
		}
	}
	if err != nil {
		apiError(&resp.Diagnostics, "network", err)
		return
	}
	labels := make([]types.String, len(n.Labels))
	for i, l := range n.Labels {
		labels[i] = types.StringValue(l)
	}
	m = networkModel{
		ID: types.Int64Value(n.ID), Name: types.StringValue(n.Name),
		Project: types.StringValue(normalize.ProjectShortName(n.ProjectName, who.AccountName)),
		Group:   types.StringValue(n.GroupName), Cloud: types.StringValue(n.CloudName),
		DisplayName: types.StringValue(n.DisplayName), Description: types.StringValue(n.Description), Labels: labels,
		Active: types.BoolValue(n.Active), Visibility: types.StringValue(n.Visibility), Status: types.StringValue(n.Status),
		CIDR: types.StringValue(n.CIDR), Gateway: types.StringValue(n.Gateway),
		DNSPrimary: types.StringValue(n.DNSPrimary), DNSSecondary: types.StringValue(n.DNSSecondary),
		DHCP: types.BoolValue(n.DHCP), SubnetID: types.Int64Value(n.SubnetID),
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

// oneNetwork picks the single network in projectID (any project when 0), or reports on attr why
// there isn't one.
func oneNetwork(all []*client.Network, attr path.Path, name string, project types.String, projectID int64, diags interface {
	AddAttributeError(path.Path, string, string)
}) *client.Network {
	var found []*client.Network
	for _, n := range all {
		if projectID == 0 || n.ProjectID == projectID {
			found = append(found, n)
		}
	}
	switch len(found) {
	case 1:
		return found[0]
	case 0:
		where := ""
		if projectID != 0 {
			where = " in project " + project.ValueString()
		}
		diags.AddAttributeError(attr, "No such network", fmt.Sprintf("No network is named %q%s. Names are matched exactly.", name, where))
	default:
		ids := make([]string, len(found))
		for i, n := range found {
			ids[i] = fmt.Sprint(n.ID)
		}
		fix := "Add project, or use the network's ID."
		if projectID != 0 {
			fix = "Use the network's ID."
		}
		diags.AddAttributeError(attr, "Several networks match",
			fmt.Sprintf("%d networks are named %q (IDs %s). %s", len(found), name, strings.Join(ids, ", "), fix))
	}
	return nil
}
