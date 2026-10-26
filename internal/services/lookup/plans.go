package lookup

import (
	"cmp"
	"context"
	"slices"

	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

func newPlans() datasource.DataSource { return &plansDataSource{} }

type plansDataSource struct{ base }

type plansModel struct {
	Project     types.String `tfsdk:"project"`
	Image       types.String `tfsdk:"image"`
	NameRegex   types.String `tfsdk:"name_regex"`
	MinCores    types.Int64  `tfsdk:"min_cores"`
	MinMemoryGB types.Int64  `tfsdk:"min_memory_gb"`
	Plans       []planModel  `tfsdk:"plans"`
}

type planModel struct {
	ID         types.Int64   `tfsdk:"id"`
	Name       types.String  `tfsdk:"name"`
	Code       types.String  `tfsdk:"code"`
	Cores      types.Int64   `tfsdk:"cores"`
	MemoryGB   types.Float64 `tfsdk:"memory_gb"`
	RootDiskGB types.Int64   `tfsdk:"root_disk_gb"`
}

const gib = 1 << 30

func (d *plansDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_plans"
}

func (d *plansDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Lists the instance sizes (plans) a project offers for an image, for example `G2S4` (2 vCPU, 4 GB). " +
			"Each project has its own copy of every size, so plan IDs differ between projects: pick plans by `name`.",
		Attributes: map[string]schema.Attribute{
			"project": projectAttribute(),
			"image": schema.StringAttribute{
				Required:    true,
				Description: "Image code the plans are for, for example `MTN-U24.04LTS`. See `mtncloud_images`.",
			},
			"name_regex": nameRegexAttribute("plans"),
			"min_cores": schema.Int64Attribute{
				Optional:    true,
				Description: "Only return plans with at least this many vCPUs.",
				Validators:  []validator.Int64{int64validator.AtLeast(1)},
			},
			"min_memory_gb": schema.Int64Attribute{
				Optional:    true,
				Description: "Only return plans with at least this much memory, in GB.",
				Validators:  []validator.Int64{int64validator.AtLeast(1)},
			},
			"plans": schema.ListNestedAttribute{
				Computed:    true,
				Description: "The plans, smallest first (by vCPUs, then memory, then name).",
				NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
					"id":           schema.Int64Attribute{Computed: true, Description: "Plan ID in this project."},
					"name":         schema.StringAttribute{Computed: true, Description: "Plan name, for example `G2S4`. Use this to pick a plan."},
					"code":         schema.StringAttribute{Computed: true, Description: "Platform code of the OpenStack flavor."},
					"cores":        schema.Int64Attribute{Computed: true, Description: "vCPUs."},
					"memory_gb":    schema.Float64Attribute{Computed: true, Description: "Memory in GB."},
					"root_disk_gb": schema.Int64Attribute{Computed: true, Description: "Default root disk size in GB. The root disk can be made larger when the instance is created."},
				}},
			},
		},
	}
}

func (d *plansDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	if !d.ready(&resp.Diagnostics) {
		return
	}
	var m plansModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	match, diags := matcher(m.NameRegex)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	project, err := d.client.ProjectByName(ctx, m.Project.ValueString())
	if err != nil {
		resp.Diagnostics.AddAttributeError(pathOf("project"), "Unknown project", err.Error())
		return
	}
	image, err := d.client.ImageByCode(ctx, m.Image.ValueString())
	if err != nil {
		resp.Diagnostics.AddAttributeError(pathOf("image"), "Unknown image", err.Error())
		return
	}
	plans, err := d.client.Plans(ctx, project.ID, image.LayoutID)
	if err != nil {
		apiError(&resp.Diagnostics, "plans", err)
		return
	}
	m.Plans = []planModel{}
	for _, p := range plans {
		if !match(p.Name) || p.Cores < m.MinCores.ValueInt64() || p.MemoryBytes < m.MinMemoryGB.ValueInt64()*gib {
			continue
		}
		m.Plans = append(m.Plans, planModel{
			ID: types.Int64Value(p.ID), Name: types.StringValue(p.Name), Code: types.StringValue(p.Code),
			Cores: types.Int64Value(p.Cores), MemoryGB: types.Float64Value(float64(p.MemoryBytes) / gib),
			RootDiskGB: types.Int64Value(p.RootBytes / gib),
		})
	}
	slices.SortFunc(m.Plans, func(a, b planModel) int {
		return cmp.Or(
			cmp.Compare(a.Cores.ValueInt64(), b.Cores.ValueInt64()),
			cmp.Compare(a.MemoryGB.ValueFloat64(), b.MemoryGB.ValueFloat64()),
			cmp.Compare(a.Name.ValueString(), b.Name.ValueString()),
		)
	})
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}
