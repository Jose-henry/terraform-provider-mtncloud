package lookup

import (
	"cmp"
	"context"
	"slices"
	"strconv"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/mtn/terraform-provider-mtncloud/internal/client"
	"github.com/mtn/terraform-provider-mtncloud/internal/normalize"
)

func newInstances() datasource.DataSource { return &instancesDataSource{} }

type instancesDataSource struct{ base }

type instancesModel struct {
	NameRegex types.String            `tfsdk:"name_regex"`
	Project   types.String            `tfsdk:"project"`
	Status    types.String            `tfsdk:"status"`
	Image     types.String            `tfsdk:"image"`
	Plan      types.String            `tfsdk:"plan"`
	Network   types.String            `tfsdk:"network"`
	Labels    []types.String          `tfsdk:"labels"`
	Tags      map[string]types.String `tfsdk:"tags"`
	Instances []instanceModel         `tfsdk:"instances"`
}

type instanceModel struct {
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
	Networks  []types.Int64  `tfsdk:"networks"`
	Labels    []types.String `tfsdk:"labels"`
	Tags      types.Map      `tfsdk:"tags"`
}

func (d *instancesDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_instances"
}

func (d *instancesDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Lists your instances (virtual machines), including those made in the console. Use an instance's " +
			"`id` to import it, or its addresses in other configuration.",
		Attributes: map[string]schema.Attribute{
			"name_regex": nameRegexAttribute("instances"),
			"project": schema.StringAttribute{
				Optional: true,
				Description: "Only return instances in this project, by name (for example `web-prod`). The full platform " +
					"name, with the tenant suffix, also works.",
			},
			"status": schema.StringAttribute{Optional: true, Description: "Only return instances with this status, for example `running` (case ignored)."},
			"image":  schema.StringAttribute{Optional: true, Description: "Only return instances of this image, by name or code (case ignored)."},
			"plan":   schema.StringAttribute{Optional: true, Description: "Only return instances of this plan, for example `G2S4` (case ignored)."},
			"network": schema.StringAttribute{
				Optional: true,
				Description: "Only return instances with a network card on this network, by ID or name (a number is tried as " +
					"an ID first). Every card counts, not only the primary one. With `project` set, the name is looked up in " +
					"that project, and only that project's instances are returned; to search every project, give the network's ID.",
			},
			"labels": schema.ListAttribute{
				Optional: true, ElementType: types.StringType,
				Description: "Only return instances that have every one of these labels (case ignored).",
			},
			"tags": schema.MapAttribute{
				Optional: true, ElementType: types.StringType,
				Description: "Only return instances that have every one of these tags with the same value.",
			},
			"instances": schema.ListNestedAttribute{
				Computed:    true,
				Description: "The instances, sorted by name, then ID.",
				NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
					"id":         schema.Int64Attribute{Computed: true, Description: "Instance ID."},
					"name":       schema.StringAttribute{Computed: true, Description: "Instance name."},
					"status":     schema.StringAttribute{Computed: true, Description: "Status, for example `running` or `stopped`."},
					"project":    schema.StringAttribute{Computed: true, Description: "Project name, without the tenant suffix."},
					"group":      schema.StringAttribute{Computed: true, Description: "Group the instance belongs to."},
					"cloud":      schema.StringAttribute{Computed: true, Description: "Cloud the instance is in, for example `MTNNG_CLOUD_AZ_1`."},
					"image":      schema.StringAttribute{Computed: true, Description: "Image name, for example `MTN Ubuntu Server 24.04.3LTS`."},
					"plan":       schema.StringAttribute{Computed: true, Description: "Plan (size) name, for example `G2S4`."},
					"private_ip": schema.StringAttribute{Computed: true, Description: "Private address, or empty."},
					"public_ip":  schema.StringAttribute{Computed: true, Description: "Floating (public) address, or empty."},
					"networks": schema.ListAttribute{
						Computed: true, ElementType: types.Int64Type,
						Description: "IDs of the networks the instance's cards are on, in the order MTN Cloud lists the cards " +
							"(the primary card first, when MTN Cloud marks one).",
					},
					"labels": schema.ListAttribute{Computed: true, ElementType: types.StringType, Description: "Labels."},
					"tags":   schema.MapAttribute{Computed: true, ElementType: types.StringType, Description: "Tags."},
				}},
			},
		},
	}
}

func (d *instancesDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	if !d.ready(&resp.Diagnostics) {
		return
	}
	var m instancesModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	match, diags := matcher(m.NameRegex)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	who, err := d.client.WhoAmI(ctx)
	if err != nil {
		apiError(&resp.Diagnostics, "instances", err)
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
	networkID := d.networkID(ctx, m, projectID, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
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
	m.Instances = []instanceModel{}
	for _, in := range instances {
		if !match(in.Name) || (projectID != 0 && in.ProjectID != projectID) || !textMatches(in.Status, m.Status) ||
			!imageMatches(in.ImageName, in.ImageCode, m.Image) || !textMatches(in.PlanName, m.Plan) ||
			!labelsMatch(in.Labels, m.Labels) || !tagsMatch(in.Tags, m.Tags) || !onNetwork(in.Interfaces, networkID) {
			continue
		}
		tags, d2 := types.MapValueFrom(ctx, types.StringType, nonNil(in.Tags))
		resp.Diagnostics.Append(d2...)
		labels := make([]types.String, len(in.Labels))
		for i, l := range in.Labels {
			labels[i] = types.StringValue(l)
		}
		networks := make([]types.Int64, len(in.Interfaces))
		for i, nic := range in.Interfaces {
			networks[i] = types.Int64Value(nic.NetworkID)
		}
		m.Instances = append(m.Instances, instanceModel{
			ID: types.Int64Value(in.ID), Name: types.StringValue(in.Name), Status: types.StringValue(in.Status),
			Project: types.StringValue(names[in.ProjectID]), Group: types.StringValue(in.GroupName), Cloud: types.StringValue(in.CloudName), Image: types.StringValue(in.ImageName),
			Plan: types.StringValue(in.PlanName), PrivateIP: types.StringValue(in.PrivateIP),
			PublicIP: types.StringValue(in.PublicIP), Networks: networks, Labels: labels, Tags: tags,
		})
	}
	slices.SortFunc(m.Instances, func(a, b instanceModel) int {
		return cmp.Or(cmp.Compare(a.Name.ValueString(), b.Name.ValueString()), cmp.Compare(a.ID.ValueInt64(), b.ID.ValueInt64()))
	})
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

// networkID resolves the network filter to an ID: 0 when it isn't set. A number is tried as an ID
// first, then as a name, so a network named "2024" can still be picked; a name must match exactly
// one network (in projectID, when set). A wrong ID is an error, like a wrong name, never an empty list.
func (d *instancesDataSource) networkID(ctx context.Context, m instancesModel, projectID int64, diags *diag.Diagnostics) int64 {
	if m.Network.IsNull() {
		return 0
	}
	name := m.Network.ValueString()
	if id, err := strconv.ParseInt(name, 10, 64); err == nil && id > 0 {
		n, err := d.client.GetNetwork(ctx, id)
		switch {
		case err == nil:
			return n.ID
		case !client.IsNotFound(err):
			apiError(diags, "networks", err)
			return 0
		}
	}
	all, err := d.client.FindNetworksNamed(ctx, name)
	if err != nil {
		apiError(diags, "networks", err)
		return 0
	}
	if n := oneNetwork(all, path.Root("network"), name, m.Project, projectID, diags); n != nil {
		return n.ID
	}
	return 0
}

// onNetwork reports whether any of the cards is on network id; every instance passes when id is 0.
func onNetwork(nics []client.Interface, id int64) bool {
	return id == 0 || slices.ContainsFunc(nics, func(n client.Interface) bool { return n.NetworkID == id })
}

// imageMatches reports whether an instance's image passes the image filter, given by name or code.
func imageMatches(name, code string, want types.String) bool {
	return textMatches(name, want) || textMatches(code, want)
}
