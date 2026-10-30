package lookup

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/datasourcevalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/mtn/terraform-provider-mtncloud/internal/client"
	"github.com/mtn/terraform-provider-mtncloud/internal/normalize"
)

func newInstance() datasource.DataSource { return &instanceDataSource{} }

type instanceDataSource struct{ base }

type instanceOneModel struct {
	ID        types.Int64    `tfsdk:"id"`
	Name      types.String   `tfsdk:"name"`
	Status    types.String   `tfsdk:"status"`
	Project   types.String   `tfsdk:"project"`
	Group     types.String   `tfsdk:"group"`
	Cloud     types.String   `tfsdk:"cloud"`
	Image     types.String   `tfsdk:"image"`
	Plan      types.String   `tfsdk:"plan"`
	PrivateIP types.String   `tfsdk:"private_ip"`
	PublicIP  types.String   `tfsdk:"public_ip"`
	Labels    []types.String `tfsdk:"labels"`
	Networks  []types.String `tfsdk:"networks"`
	SGs       []types.String `tfsdk:"security_groups"`
	Volumes   types.List     `tfsdk:"volumes"`
}

var instanceVolumeType = types.ObjectType{AttrTypes: map[string]attr.Type{
	"name": types.StringType, "size": types.Int64Type, "root": types.BoolType,
}}

func (d *instanceDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_instance"
}

func (d *instanceDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Looks up one instance (virtual machine) by ID, or by name, including one made in the console. " +
			"A name must match exactly one instance; add `project` when the same name is used in several projects.",
		Attributes: map[string]schema.Attribute{
			// Optional+Computed: give id or name, and the other is filled in.
			"id": schema.Int64Attribute{Optional: true, Computed: true, Description: "Instance ID. Give this or `name`."},
			"name": schema.StringAttribute{
				Optional: true, Computed: true, Description: "Instance name, matched exactly. Give this or `id`.",
			},
			// Optional+Computed: narrows a name lookup, and is filled in from the instance either way.
			"project": schema.StringAttribute{
				Optional: true, Computed: true,
				Description: "Project name (for example `web-prod`), to tell apart instances with the same name. " +
					"The full platform name, with the tenant suffix, also works. Always set to the instance's project, without the suffix.",
			},
			"status":     schema.StringAttribute{Computed: true, Description: "Status, for example `running` or `stopped`."},
			"group":      schema.StringAttribute{Computed: true, Description: "Group the instance belongs to."},
			"cloud":      schema.StringAttribute{Computed: true, Description: "Cloud the instance is in, for example `MTNNG_CLOUD_AZ_1`."},
			"image":      schema.StringAttribute{Computed: true, Description: "Image name, for example `MTN Ubuntu Server 24.04.3LTS`."},
			"plan":       schema.StringAttribute{Computed: true, Description: "Plan (size) name, for example `G2S4`."},
			"private_ip": schema.StringAttribute{Computed: true, Description: "Private address, or empty."},
			"public_ip":  schema.StringAttribute{Computed: true, Description: "Floating (public) address, or empty."},
			"labels":     schema.ListAttribute{Computed: true, ElementType: types.StringType, Description: "Labels."},
			"networks": schema.ListAttribute{
				Computed: true, ElementType: types.StringType,
				Description: "Network IDs of the instance's network cards, primary first, as `mtncloud_instance` takes them.",
			},
			"security_groups": schema.ListAttribute{
				Computed: true, ElementType: types.StringType,
				Description: "Names of the security groups applied to the instance, sorted, the project's default group included. " +
					"Null, with a warning, when your role can't read them.",
			},
			"volumes": schema.ListNestedAttribute{
				Computed: true, Description: "Disks, root first.",
				NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
					"name": schema.StringAttribute{Computed: true, Description: "Disk name."},
					"size": schema.Int64Attribute{Computed: true, Description: "Size in GB."},
					"root": schema.BoolAttribute{Computed: true, Description: "Whether this is the root (boot) disk."},
				}},
			},
		},
	}
}

func (d *instanceDataSource) ConfigValidators(context.Context) []datasource.ConfigValidator {
	return []datasource.ConfigValidator{datasourcevalidator.ExactlyOneOf(path.MatchRoot("id"), path.MatchRoot("name"))}
}

func (d *instanceDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	if !d.ready(&resp.Diagnostics) {
		return
	}
	var m instanceOneModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	who, err := d.client.WhoAmI(ctx)
	if err != nil {
		apiError(&resp.Diagnostics, "instance", err)
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
	var in *client.Instance
	if !m.ID.IsNull() {
		in, err = d.client.GetInstance(ctx, m.ID.ValueInt64())
		if client.IsNotFound(err) {
			resp.Diagnostics.AddAttributeError(path.Root("id"), "No such instance", fmt.Sprintf("There is no instance %d you can see.", m.ID.ValueInt64()))
			return
		}
		if err == nil && projectID != 0 && in.ProjectID != 0 && in.ProjectID != projectID {
			resp.Diagnostics.AddAttributeError(path.Root("project"), "Instance in another project",
				fmt.Sprintf("Instance %d isn't in project %s. Remove project, or fix it.", in.ID, m.Project.ValueString()))
			return
		}
	} else {
		in, err = d.findByName(ctx, m.Name.ValueString(), m.Project, projectID, &resp.Diagnostics)
	}
	if err != nil {
		apiError(&resp.Diagnostics, "instance", err)
		return
	}
	if in == nil {
		return // findByName reported why
	}
	project := ""
	if projects, err := d.client.Projects(ctx); err == nil {
		for _, p := range projects {
			if p.ID == in.ProjectID {
				project = normalize.ProjectShortName(p.Name, who.AccountName)
			}
		}
	} else {
		apiError(&resp.Diagnostics, "projects", err)
		return
	}
	// Security groups are a separate call: a role that can read instances but not their groups still
	// gets the instance, with security_groups null and a warning.
	var sgs []types.String
	if groups, err := d.client.InstanceSecurityGroups(ctx, in.ID); err != nil {
		resp.Diagnostics.AddAttributeWarning(path.Root("security_groups"), "Security groups not read", err.Error())
	} else {
		sgs = make([]types.String, 0, len(groups))
		for _, g := range groups {
			sgs = append(sgs, types.StringValue(g.Name))
		}
	}
	slices.SortFunc(sgs, func(a, b types.String) int { return strings.Compare(a.ValueString(), b.ValueString()) })
	nets := make([]types.String, len(in.Interfaces))
	for i, nic := range in.Interfaces {
		nets[i] = types.StringValue(fmt.Sprint(nic.NetworkID))
	}
	vols := make([]attr.Value, len(in.Volumes))
	for i, v := range in.Volumes {
		vols[i] = types.ObjectValueMust(instanceVolumeType.AttrTypes, map[string]attr.Value{
			"name": types.StringValue(v.Name), "size": types.Int64Value(v.Size), "root": types.BoolValue(v.Root)})
	}
	volumes, diags := types.ListValue(instanceVolumeType, vols)
	resp.Diagnostics.Append(diags...)
	labels := make([]types.String, len(in.Labels))
	for i, l := range in.Labels {
		labels[i] = types.StringValue(l)
	}
	m = instanceOneModel{
		ID: types.Int64Value(in.ID), Name: types.StringValue(in.Name), Status: types.StringValue(in.Status),
		Project: types.StringValue(project), Group: types.StringValue(in.GroupName), Cloud: types.StringValue(in.CloudName), Image: types.StringValue(in.ImageName), Plan: types.StringValue(in.PlanName),
		PrivateIP: types.StringValue(in.PrivateIP), PublicIP: types.StringValue(in.PublicIP), Labels: labels,
		Networks: nets, SGs: sgs, Volumes: volumes,
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

// findByName returns the one instance with this name (in projectID, when not 0), or reports why
// there isn't exactly one.
func (d *instanceDataSource) findByName(ctx context.Context, name string, project types.String, projectID int64, diags interface {
	AddAttributeError(path.Path, string, string)
}) (*client.Instance, error) {
	all, err := d.client.ListInstancesNamed(ctx, name)
	if err != nil {
		return nil, err
	}
	var found []*client.Instance
	for _, in := range all {
		if in.Name == name && (projectID == 0 || in.ProjectID == projectID) {
			found = append(found, in)
		}
	}
	switch len(found) {
	case 1:
		return found[0], nil
	case 0:
		where := ""
		if projectID != 0 {
			where = " in project " + project.ValueString()
		}
		diags.AddAttributeError(path.Root("name"), "No such instance",
			fmt.Sprintf("No instance is named %q%s. Names are matched exactly; mtncloud_instances lists them.", name, where))
	default:
		fix := "Add project, or look one up by id."
		if projectID != 0 {
			fix = "Look one up by id."
		}
		ids := make([]string, len(found))
		for i, in := range found {
			ids[i] = fmt.Sprint(in.ID)
		}
		diags.AddAttributeError(path.Root("name"), "Several instances match",
			fmt.Sprintf("%d instances are named %q (IDs %s). %s", len(found), name, strings.Join(ids, ", "), fix))
	}
	return nil, nil
}
