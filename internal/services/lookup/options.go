package lookup

import (
	"cmp"
	"context"
	"slices"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/mtn/terraform-provider-mtncloud/internal/client"
)

// The datastore, security group and external network lookups are the same shape: an optional
// project, a name filter, and a sorted list of {id, name[, external_id]}. optionList implements
// all three; each one only describes itself.

type optionListSpec struct {
	typeName    string // after "mtncloud_"
	listAttr    string // name of the list attribute, for example "datastores"
	what        string // plural noun for messages, for example "datastores"
	description string
	idDesc      string
	nameDesc    string
	externalID  string // description of external_id; empty means the items have none
	byProject   bool
	list        func(ctx context.Context, c *client.Client, projectID int64) ([]client.Option, error)
}

type optionListDataSource struct {
	base
	spec optionListSpec
}

func (d *optionListDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_" + d.spec.typeName
}

func (d *optionListDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	item := map[string]schema.Attribute{
		"id":   schema.Int64Attribute{Computed: true, Description: d.spec.idDesc},
		"name": schema.StringAttribute{Computed: true, Description: d.spec.nameDesc},
	}
	if d.spec.externalID != "" {
		item["external_id"] = schema.StringAttribute{Computed: true, Description: d.spec.externalID}
	}
	attrs := map[string]schema.Attribute{
		"name_regex": nameRegexAttribute(d.spec.what),
		d.spec.listAttr: schema.ListNestedAttribute{
			Computed:     true,
			Description:  "The " + d.spec.what + ", sorted by name.",
			NestedObject: schema.NestedAttributeObject{Attributes: item},
		},
	}
	if d.spec.byProject {
		attrs["project"] = projectAttribute()
	}
	resp.Schema = schema.Schema{Description: d.spec.description, Attributes: attrs}
}

type optionItem struct {
	ID         types.Int64  `tfsdk:"id"`
	Name       types.String `tfsdk:"name"`
	ExternalID types.String `tfsdk:"external_id"`
}

func (d *optionListDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	if !d.ready(&resp.Diagnostics) {
		return
	}
	var regex, project types.String
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("name_regex"), &regex)...)
	if d.spec.byProject {
		resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("project"), &project)...)
	}
	match, diags := matcher(regex)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	var projectID int64
	if d.spec.byProject {
		p, err := d.client.ProjectByName(ctx, project.ValueString())
		if err != nil {
			resp.Diagnostics.AddAttributeError(path.Root("project"), client.ProjectErrorTitle(err), err.Error())
			return
		}
		projectID = p.ID
	}
	opts, err := d.spec.list(ctx, d.client, projectID)
	if err != nil {
		apiError(&resp.Diagnostics, d.spec.what, err)
		return
	}

	items := []optionItem{}
	for _, o := range opts {
		if match(o.Name) {
			items = append(items, optionItem{ID: types.Int64Value(o.ID), Name: types.StringValue(o.Name), ExternalID: types.StringValue(o.ExternalID)})
		}
	}
	slices.SortFunc(items, func(a, b optionItem) int {
		return cmp.Or(cmp.Compare(a.Name.ValueString(), b.Name.ValueString()), cmp.Compare(a.ID.ValueInt64(), b.ID.ValueInt64()))
	})

	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("name_regex"), regex)...)
	if d.spec.byProject {
		resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("project"), project)...)
	}
	if d.spec.externalID != "" {
		resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root(d.spec.listAttr), items)...)
		return
	}
	type plain struct {
		ID   types.Int64  `tfsdk:"id"`
		Name types.String `tfsdk:"name"`
	}
	out := make([]plain, len(items))
	for i, it := range items {
		out[i] = plain{ID: it.ID, Name: it.Name}
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root(d.spec.listAttr), out)...)
}

func pathOf(attr string) path.Path { return path.Root(attr) }

func newDatastores() datasource.DataSource {
	return &optionListDataSource{spec: optionListSpec{
		typeName: "datastores", listAttr: "datastores", what: "datastores", byProject: true,
		description: "Lists the storage datastores a project can put volumes on, for example `__DEFAULT__`, " +
			"the default datastore. " +
			"Which ones you see depends on your tenant; new datastores appear here automatically.",
		idDesc:   "Datastore ID.",
		nameDesc: "Datastore name. Use this to choose where a volume is stored.",
		list: func(ctx context.Context, c *client.Client, projectID int64) ([]client.Option, error) {
			return c.Datastores(ctx, projectID)
		},
	}}
}

func newSecurityGroups() datasource.DataSource {
	return &optionListDataSource{spec: optionListSpec{
		typeName: "security_groups", listAttr: "security_groups", what: "security groups", byProject: true,
		description: "Lists the security groups in a project, including the `default` group MTN Cloud creates with every project. " +
			"Several projects can each have a group with the same name, which is why `project` is required.",
		idDesc:     "Security group ID.",
		nameDesc:   "Security group name.",
		externalID: "Security group ID in the cloud (the external ID).",
		list: func(ctx context.Context, c *client.Client, projectID int64) ([]client.Option, error) {
			return c.SecurityGroups(ctx, projectID)
		},
	}}
}

func newExternalNetworks() datasource.DataSource {
	return &optionListDataSource{spec: optionListSpec{
		typeName: "external_networks", listAttr: "external_networks", what: "external networks",
		description: "Lists the external networks an MTN Router can connect to, for example `public-network-01`. " +
			"A router on an external network gives the instances behind it a route to the internet.",
		idDesc:   "Network ID.",
		nameDesc: "Network name. Use this to choose a router's external network.",
		list: func(ctx context.Context, c *client.Client, _ int64) ([]client.Option, error) {
			return c.ExternalNetworks(ctx)
		},
	}}
}

func newRegistries() datasource.DataSource {
	return &optionListDataSource{spec: optionListSpec{
		typeName: "registries", listAttr: "registries", what: "registries",
		description: "Lists the container registries a Kubernetes cluster can pull images from (the console's \"Cluster Repo Account\"). " +
			"Use a name as `mtncloud_cluster`'s `registry`. The public Docker Hub is always listed, and is the default.",
		idDesc:   "Registry ID; 0 for the public Docker Hub.",
		nameDesc: "Registry name.",
		list: func(ctx context.Context, c *client.Client, _ int64) ([]client.Option, error) {
			place, err := c.Placement(ctx)
			if err != nil {
				return nil, err
			}
			regs, err := c.Registries(ctx, place)
			out := make([]client.Option, 0, len(regs))
			for _, r := range regs {
				out = append(out, client.Option{ID: r.ID, Name: r.Name})
			}
			return out, err
		},
	}}
}

func newClusterLayouts() datasource.DataSource {
	return &optionListDataSource{spec: optionListSpec{
		typeName: "cluster_layouts", listAttr: "cluster_layouts", what: "cluster layouts",
		description: "Lists MTN's cluster layouts: `MTN Kubernetes Single-Master`, `MTN Kubernetes HA (3)`, `MTN Kubernetes HA (5)` " +
			"and `MTN Docker Cluster`. Use a name as `mtncloud_cluster`'s `layout`.",
		idDesc:   "Layout ID.",
		nameDesc: "Layout name.",
		list: func(ctx context.Context, c *client.Client, _ int64) ([]client.Option, error) {
			layouts, err := c.ClusterLayouts(ctx)
			out := make([]client.Option, 0, len(layouts))
			for _, l := range layouts {
				out = append(out, client.Option{ID: l.ID, Name: l.Name})
			}
			return out, err
		},
	}}
}
