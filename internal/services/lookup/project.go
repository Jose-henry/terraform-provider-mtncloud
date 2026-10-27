package lookup

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework-validators/datasourcevalidator"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/mtn/terraform-provider-mtncloud/internal/client"
	"github.com/mtn/terraform-provider-mtncloud/internal/normalize"
)

func newProject() datasource.DataSource { return &projectDataSource{} }

// projectDataSource is mtncloud_project: one project, with the group and cloud it is in.
type projectDataSource struct{ base }

type oneProjectModel struct {
	ID         types.Int64  `tfsdk:"id"`
	Name       types.String `tfsdk:"name"`
	FullName   types.String `tfsdk:"full_name"`
	ExternalID types.String `tfsdk:"external_id"`
	Group      types.String `tfsdk:"group"`
	Cloud      types.String `tfsdk:"cloud"`
}

func (d *projectDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_project"
}

func (d *projectDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Looks up one project by ID or by name, with the group and cloud it is in. A project is where instances, " +
			"MTN Networks, MTN Routers and security groups live. To list them, use `mtncloud_projects`.",
		Attributes: map[string]schema.Attribute{
			// Optional+Computed: give id or name, and the other is filled in.
			"id": schema.Int64Attribute{Optional: true, Computed: true, Description: "Project ID. Give this or `name`."},
			"name": schema.StringAttribute{
				Optional: true, Computed: true,
				Description: "Project name as shown in the console, for example `web-prod`; the full platform name, with the tenant " +
					"suffix, also works. Give this or `id`. Always set to the name without the suffix.",
			},
			"full_name":   schema.StringAttribute{Computed: true, Description: "Full platform name, with the tenant suffix, for example `web-prod-Acme`."},
			"external_id": schema.StringAttribute{Computed: true, Description: "Project ID in the cloud (the external ID)."},
			"group":       schema.StringAttribute{Computed: true, Description: "Group the project is in."},
			"cloud":       schema.StringAttribute{Computed: true, Description: "Cloud the project is in, for example `MTNNG_CLOUD_AZ_1`."},
		},
	}
}

func (d *projectDataSource) ConfigValidators(context.Context) []datasource.ConfigValidator {
	return []datasource.ConfigValidator{datasourcevalidator.ExactlyOneOf(path.MatchRoot("id"), path.MatchRoot("name"))}
}

func (d *projectDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	if !d.ready(&resp.Diagnostics) {
		return
	}
	var m oneProjectModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	who, err := d.client.WhoAmI(ctx)
	if err != nil {
		apiError(&resp.Diagnostics, "projects", err)
		return
	}
	place, err := d.client.Placement(ctx)
	if err != nil {
		apiError(&resp.Diagnostics, "your group and cloud", err)
		return
	}
	var p client.Option
	if !m.Name.IsNull() {
		if p, err = d.client.ProjectByName(ctx, m.Name.ValueString()); err != nil {
			if client.IsNoMatch(err) {
				resp.Diagnostics.AddAttributeError(path.Root("name"), "No such project", err.Error())
			} else {
				apiError(&resp.Diagnostics, "projects", err)
			}
			return
		}
	} else {
		all, err := d.client.Projects(ctx)
		if err != nil {
			apiError(&resp.Diagnostics, "projects", err)
			return
		}
		found := false
		for _, x := range all {
			if x.ID == m.ID.ValueInt64() {
				p, found = x, true
			}
		}
		if !found {
			resp.Diagnostics.AddAttributeError(path.Root("id"), "No such project",
				fmt.Sprintf("There is no project %d you can deploy into; mtncloud_projects lists them.", m.ID.ValueInt64()))
			return
		}
	}
	m = oneProjectModel{
		ID: types.Int64Value(p.ID), Name: types.StringValue(normalize.ProjectShortName(p.Name, who.AccountName)),
		FullName: types.StringValue(p.Name), ExternalID: types.StringValue(p.ExternalID),
		Group: types.StringValue(place.GroupName), Cloud: types.StringValue(place.CloudName),
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}
