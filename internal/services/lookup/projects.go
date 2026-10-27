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

func newProjects() datasource.DataSource { return &projectsDataSource{} }

type projectsDataSource struct{ base }

type projectsModel struct {
	NameRegex types.String   `tfsdk:"name_regex"`
	Projects  []projectModel `tfsdk:"projects"`
}

type projectModel struct {
	ID         types.Int64  `tfsdk:"id"`
	Name       types.String `tfsdk:"name"`
	FullName   types.String `tfsdk:"full_name"`
	ExternalID types.String `tfsdk:"external_id"`
}

func (d *projectsDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_projects"
}

func (d *projectsDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Lists the projects you can deploy into. A project is where instances, MTN Networks, MTN Routers and security groups live. " +
			"Use a project's `name` anywhere a resource asks for `project`.",
		Attributes: map[string]schema.Attribute{
			"name_regex": schema.StringAttribute{
				Optional:    true,
				Description: "Only return projects whose name (the short name, without the tenant suffix) matches this regular expression (RE2 syntax), for example `^web-`.",
				Validators:  nameRegexAttribute("projects").Validators,
			},
			"projects": schema.ListNestedAttribute{
				Computed:    true,
				Description: "The projects, sorted by name.",
				NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
					"id":          schema.Int64Attribute{Computed: true, Description: "Project ID."},
					"name":        schema.StringAttribute{Computed: true, Description: "Project name as shown in the console, for example `web-prod`."},
					"full_name":   schema.StringAttribute{Computed: true, Description: "Full platform name, with the tenant suffix, for example `web-prod-Acme`."},
					"external_id": schema.StringAttribute{Computed: true, Description: "Project ID in the cloud (the external ID)."},
				}},
			},
		},
	}
}

func (d *projectsDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	if !d.ready(&resp.Diagnostics) {
		return
	}
	var m projectsModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	match, diags := matcher(m.NameRegex)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	who, err := d.client.WhoAmI(ctx)
	if err != nil {
		apiError(&resp.Diagnostics, "projects", err)
		return
	}
	projects, err := d.client.Projects(ctx)
	if err != nil {
		apiError(&resp.Diagnostics, "projects", err)
		return
	}
	m.Projects = []projectModel{}
	for _, p := range projects {
		short := normalize.ProjectShortName(p.Name, who.AccountName)
		if !match(short) {
			continue
		}
		m.Projects = append(m.Projects, projectModel{
			ID: types.Int64Value(p.ID), Name: types.StringValue(short),
			FullName: types.StringValue(p.Name), ExternalID: types.StringValue(p.ExternalID),
		})
	}
	slices.SortFunc(m.Projects, func(a, b projectModel) int {
		return cmp.Or(cmp.Compare(a.Name.ValueString(), b.Name.ValueString()), cmp.Compare(a.FullName.ValueString(), b.FullName.ValueString()))
	})
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}
