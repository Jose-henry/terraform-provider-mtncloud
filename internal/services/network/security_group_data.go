package network

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
	"github.com/mtn/terraform-provider-mtncloud/internal/normalize"
)

// DataSources lists the data sources that share this package's flattening with its resources.
func DataSources() []func() datasource.DataSource {
	return []func() datasource.DataSource{newSecurityGroupData}
}

// dataBase is base for data sources: the same client, from a data source's Configure.
type dataBase struct{ base }

func (b *dataBase) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	c, ok := req.ProviderData.(*client.Client)
	if !ok {
		resp.Diagnostics.AddError("Unexpected provider data", fmt.Sprintf("expected *client.Client, got %T. This is a provider bug.", req.ProviderData))
		return
	}
	b.client = c
}

func newSecurityGroupData() datasource.DataSource { return &securityGroupData{} }

// securityGroupData is mtncloud_security_group: one group, with every rule, as the resource's
// all_rules shows them.
type securityGroupData struct{ dataBase }

type securityGroupDataModel struct {
	ID          types.String `tfsdk:"id"`
	Name        types.String `tfsdk:"name"`
	Project     types.String `tfsdk:"project"`
	Description types.String `tfsdk:"description"`
	ExternalID  types.String `tfsdk:"external_id"`
	Rules       types.List   `tfsdk:"rules"`
}

func (d *securityGroupData) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_security_group"
}

func (d *securityGroupData) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Looks up one security group by ID or by name, including one made in the console, with its project and every " +
			"rule. A name must match exactly one group; add `project` when the same name is used in several projects. To list " +
			"the groups of a project, use `mtncloud_security_groups`.",
		Attributes: map[string]schema.Attribute{
			// Optional+Computed: give id or name, and the other is filled in.
			"id":   schema.StringAttribute{Optional: true, Computed: true, Description: "Security group ID. Give this or `name`."},
			"name": schema.StringAttribute{Optional: true, Computed: true, Description: "Security group name, matched exactly. Give this or `id`."},
			// Optional+Computed: narrows a name lookup, and is filled in from the group either way.
			"project": schema.StringAttribute{
				Optional: true, Computed: true,
				Description: "Project name (for example `web-prod`), to tell apart groups with the same name. The full platform " +
					"name, with the tenant suffix, also works. Always set to the group's project, without the suffix.",
			},
			"description": schema.StringAttribute{Computed: true, Description: "Description, or empty."},
			"external_id": schema.StringAttribute{Computed: true, Description: "Security group ID in the cloud (the external ID)."},
			"rules": schema.ListNestedAttribute{
				Computed:    true,
				Description: "Every rule of the group, including the default egress rules, sorted by what they allow.",
				NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
					"id":                       schema.StringAttribute{Computed: true, Description: "Rule ID."},
					"direction":                schema.StringAttribute{Computed: true, Description: "`ingress` or `egress`."},
					"protocol":                 schema.StringAttribute{Computed: true, Description: "`tcp`, `udp`, `icmp` or `any`."},
					"port_range":               schema.StringAttribute{Computed: true, Description: "Port or range; null for all ports."},
					"cidr":                     schema.StringAttribute{Computed: true, Description: "Remote addresses; null for any address or a group rule."},
					"source_security_group_id": schema.StringAttribute{Computed: true, Description: "Remote security group, for group rules."},
					"description":              schema.StringAttribute{Computed: true, Description: "Rule name in the console."},
					"default":                  schema.BoolAttribute{Computed: true, Description: "Whether this is one of the default egress rules MTN Cloud adds."},
				}},
			},
		},
	}
}

func (d *securityGroupData) ConfigValidators(context.Context) []datasource.ConfigValidator {
	return []datasource.ConfigValidator{datasourcevalidator.ExactlyOneOf(path.MatchRoot("id"), path.MatchRoot("name"))}
}

func (d *securityGroupData) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	if !d.ready(&resp.Diagnostics) {
		return
	}
	var m securityGroupDataModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	tenant := d.tenant(ctx, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	var projectID int64
	if !m.Project.IsNull() {
		p, err := d.client.ProjectByName(ctx, m.Project.ValueString())
		if err != nil {
			if client.IsNoMatch(err) {
				resp.Diagnostics.AddAttributeError(path.Root("project"), client.ProjectErrorTitle(err), err.Error())
			} else {
				resp.Diagnostics.AddError("Unable to read projects", err.Error())
			}
			return
		}
		projectID = p.ID
	}
	inProject := func(g *client.SecurityGroup) bool {
		if projectID == 0 {
			return true
		}
		for _, l := range g.Locations {
			if l.ProjectID == projectID {
				return true
			}
		}
		return false
	}

	var g *client.SecurityGroup
	if !m.ID.IsNull() {
		id, err := strconv.ParseInt(m.ID.ValueString(), 10, 64)
		if err != nil {
			resp.Diagnostics.AddAttributeError(path.Root("id"), "Invalid security group ID", fmt.Sprintf("%q isn't a number.", m.ID.ValueString()))
			return
		}
		g, err = d.client.GetSecurityGroup(ctx, id)
		if client.IsNotFound(err) {
			resp.Diagnostics.AddAttributeError(path.Root("id"), "No such security group", fmt.Sprintf("There is no security group %d you can see.", id))
			return
		}
		if err != nil {
			resp.Diagnostics.AddError("Unable to read the security group", err.Error())
			return
		}
		if !inProject(g) {
			resp.Diagnostics.AddAttributeError(path.Root("project"), "Security group in another project",
				fmt.Sprintf("Security group %d isn't in project %s. Remove project, or fix it.", id, m.Project.ValueString()))
			return
		}
	} else {
		all, err := d.client.FindSecurityGroupsNamed(ctx, m.Name.ValueString())
		if err != nil {
			resp.Diagnostics.AddError("Unable to read security groups", err.Error())
			return
		}
		var found []*client.SecurityGroup
		for _, x := range all {
			if inProject(x) {
				found = append(found, x)
			}
		}
		switch len(found) {
		case 1:
			g = found[0]
		case 0:
			where := ""
			if projectID != 0 {
				where = " in project " + m.Project.ValueString()
			}
			resp.Diagnostics.AddAttributeError(path.Root("name"), "No such security group",
				fmt.Sprintf("No security group is named %q%s. Names are matched exactly; mtncloud_security_groups lists a project's groups.", m.Name.ValueString(), where))
			return
		default:
			ids := make([]string, len(found))
			for i, x := range found {
				ids[i] = strconv.FormatInt(x.ID, 10)
			}
			fix := "Add project, or look one up by id."
			if projectID != 0 {
				fix = "Look one up by id."
			}
			resp.Diagnostics.AddAttributeError(path.Root("name"), "Several security groups match",
				fmt.Sprintf("%d security groups are named %q (IDs %s). %s", len(found), m.Name.ValueString(), strings.Join(ids, ", "), fix))
			return
		}
	}

	m.ID = types.StringValue(strconv.FormatInt(g.ID, 10))
	m.Name = types.StringValue(g.Name)
	m.Description = types.StringValue(g.Description)
	m.ExternalID = types.StringValue(g.ExternalID)
	m.Project = types.StringValue("")
	if len(g.Locations) > 0 {
		if m.ExternalID.ValueString() == "" {
			m.ExternalID = types.StringValue(g.Locations[0].ExternalID)
		}
		m.Project = types.StringValue(normalize.ProjectShortName(g.Locations[0].ProjectName, tenant))
	}
	rules, diags := allRules(ctx, g.Rules)
	resp.Diagnostics.Append(diags...)
	m.Rules = rules
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}
