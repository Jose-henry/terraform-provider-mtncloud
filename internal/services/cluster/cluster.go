// Package cluster holds mtncloud_cluster: Kubernetes and Docker clusters built from MTN's own
// cluster layouts.
package cluster

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-framework-timeouts/resource/timeouts"
	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/listvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-framework/types/basetypes"

	"github.com/mtn/terraform-provider-mtncloud/internal/client"
	"github.com/mtn/terraform-provider-mtncloud/internal/planmodifiers"
	"github.com/mtn/terraform-provider-mtncloud/internal/validators"
)

// scalerPending, in private state, marks a cluster whose autoscaler still has to be configured:
// the create built it but couldn't configure the autoscaler. It is private because autoscaling is
// pure configuration, which an apply can't change.
const scalerPending = "autoscaler_pending"

const (
	createTimeout = 90 * time.Minute
	changeTimeout = 45 * time.Minute
)

// Resources lists this package's resources.
func Resources() []func() resource.Resource {
	return []func() resource.Resource{func() resource.Resource { return &clusterResource{} }}
}

type clusterResource struct{ client *client.Client }

// typeCodes maps the type argument to the platform's cluster type code.
var typeCodes = map[string]string{"kubernetes": "kubernetes-cluster", "docker": "docker-cluster"}

var defaultLayouts = map[string]string{"kubernetes": "MTN Kubernetes Single-Master", "docker": "MTN Docker Cluster"}

type model struct {
	ID                types.String   `tfsdk:"id"`
	Name              types.String   `tfsdk:"name"`
	Type              types.String   `tfsdk:"type"`
	Layout            types.String   `tfsdk:"layout"`
	Group             types.String   `tfsdk:"group"`
	Cloud             types.String   `tfsdk:"cloud"`
	Project           types.String   `tfsdk:"project"`
	Description       types.String   `tfsdk:"description"`
	Labels            types.List     `tfsdk:"labels"`
	ResourceName      types.String   `tfsdk:"resource_name"`
	Hostname          types.String   `tfsdk:"hostname"`
	Domain            types.String   `tfsdk:"domain"`
	KubernetesVersion types.String   `tfsdk:"kubernetes_version"`
	PodCIDR           types.String   `tfsdk:"pod_cidr"`
	ServiceCIDR       types.String   `tfsdk:"service_cidr"`
	Registry          types.String   `tfsdk:"registry"`
	Workflow          types.String   `tfsdk:"workflow"`
	CreateUser        types.Bool     `tfsdk:"create_user"`
	Master            types.Object   `tfsdk:"master"`
	WorkerCount       types.Int64    `tfsdk:"worker_count"`
	Worker            types.Object   `tfsdk:"worker"`
	Autoscaling       types.Object   `tfsdk:"autoscaling"`
	Status            types.String   `tfsdk:"status"`
	APIURL            types.String   `tfsdk:"api_url"`
	WorkerIDs         types.List     `tfsdk:"worker_ids"`
	Timeouts          timeouts.Value `tfsdk:"timeouts"`
}

// node is the master block, and the worker block's overrides (every field optional there).
type node struct {
	Plan           types.String `tfsdk:"plan"`
	RootVolumeSize types.Int64  `tfsdk:"root_volume_size"`
	Datastore      types.String `tfsdk:"datastore"`
	DataVolumes    []volume     `tfsdk:"data_volume"`
	Networks       []string     `tfsdk:"networks"`
	SecurityGroups []string     `tfsdk:"security_groups"`
	FloatingIPPool types.String `tfsdk:"floating_ip_pool"`
}

type volume struct {
	Size      types.Int64  `tfsdk:"size"`
	Datastore types.String `tfsdk:"datastore"`
}

type autoscaling struct {
	MinNodes types.Int64 `tfsdk:"min_nodes"`
	MaxNodes types.Int64 `tfsdk:"max_nodes"`
}

func (r *clusterResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_cluster"
}

// replaceIfSet replaces the cluster when a value it was built with changes. An imported cluster has
// nothing in state yet: adopting its configuration isn't a change.
func replaceIfSet() planmodifier.String {
	return stringplanmodifier.RequiresReplaceIf(func(_ context.Context, req planmodifier.StringRequest, resp *stringplanmodifier.RequiresReplaceIfFuncResponse) {
		resp.RequiresReplace = !req.StateValue.IsNull()
	}, "Changing it builds a new cluster.", "Changing it builds a new cluster.")
}

type objectReplaceIfSet struct{}

func (objectReplaceIfSet) Description(context.Context) string {
	return "Changing it builds a new cluster."
}
func (objectReplaceIfSet) MarkdownDescription(ctx context.Context) string {
	return objectReplaceIfSet{}.Description(ctx)
}
func (objectReplaceIfSet) PlanModifyObject(_ context.Context, req planmodifier.ObjectRequest, resp *planmodifier.ObjectResponse) {
	if !req.StateValue.IsNull() && !req.PlanValue.Equal(req.StateValue) {
		resp.RequiresReplace = true
	}
}

func nodeAttrs(master bool) map[string]schema.Attribute {
	who := "master"
	if !master {
		who = "worker"
	}
	inherit := ""
	if !master {
		inherit = " Left out, the master's is used."
	}
	ds := func(what string) schema.StringAttribute {
		return schema.StringAttribute{Optional: true, Description: "Datastore (storage type) of the " + what + ", by name. Left out, the project's default."}
	}
	return map[string]schema.Attribute{
		"plan": schema.StringAttribute{Required: master, Optional: !master,
			Description: "Plan (size) of each " + who + " node, by name, for example `G4S16`." + inherit},
		"root_volume_size": schema.Int64Attribute{Required: master, Optional: !master,
			Description: "Root disk size in GB." + inherit, Validators: []validator.Int64{int64validator.AtLeast(1)}},
		"datastore": ds("root disk"),
		"data_volume": schema.ListNestedAttribute{Required: master, Optional: !master,
			Description: "Data disks, at least one (MTN's layouts keep container data on it)." + inherit,
			Validators:  []validator.List{listvalidator.SizeAtLeast(1)},
			NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
				"size":      schema.Int64Attribute{Required: true, Description: "Size in GB.", Validators: []validator.Int64{int64validator.AtLeast(1)}},
				"datastore": ds("disk"),
			}}},
		"networks": schema.ListAttribute{Required: master, Optional: !master, ElementType: types.StringType,
			Description: "MTN Networks of the project, by name; the first is the primary interface." + inherit,
			Validators:  []validator.List{listvalidator.SizeAtLeast(1)}},
		"security_groups": schema.ListAttribute{Optional: true, ElementType: types.StringType,
			Description: "Security groups, by name. Left out, the project's default security group." + inherit},
		"floating_ip_pool": schema.StringAttribute{Optional: true,
			Description: "Floating IP pool (public network) to give each node a public IP from, for example `public-network-01`." + inherit},
	}
}

func (r *clusterResource) Schema(ctx context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	keep := []planmodifier.String{stringplanmodifier.UseStateForUnknown()}
	built := func(desc string) schema.StringAttribute {
		return schema.StringAttribute{Optional: true, Description: desc, PlanModifiers: []planmodifier.String{replaceIfSet()}}
	}
	// Optional+Computed: the user may set it, otherwise a default (the layout's, or the provider's) is planned.
	defaulted := func(desc string, v ...validator.String) schema.StringAttribute {
		return schema.StringAttribute{Optional: true, Computed: true, Description: desc, Validators: v,
			PlanModifiers: []planmodifier.String{replaceIfSet(), stringplanmodifier.UseStateForUnknown()}}
	}
	cidr := validators.IPv4CIDR()
	resp.Schema = schema.Schema{
		Description: "A Kubernetes or Docker cluster, built from MTN's cluster layouts. The masters (Docker: the hosts) are built " +
			"from `master`; workers use `worker` where it is set and the master's settings otherwise.",
		Attributes: map[string]schema.Attribute{
			"id":   schema.StringAttribute{Computed: true, Description: "Cluster ID.", PlanModifiers: keep},
			"name": schema.StringAttribute{Required: true, Description: "Cluster name. Changing it renames the cluster in place."},
			"type": defaulted("`kubernetes` (default) or `docker`. Changing it builds a new cluster.",
				stringvalidator.OneOf("kubernetes", "docker")),
			"layout": defaulted("MTN layout, by name: `MTN Kubernetes Single-Master` (default for Kubernetes), `MTN Kubernetes HA (3)`, " +
				"`MTN Kubernetes HA (5)` or `MTN Docker Cluster` (Docker). Changing it builds a new cluster."),
			"group":       defaulted("Group, by name. Left out, the provider's `group` (or `MTNCLOUD_GROUP`)."),
			"cloud":       defaulted("Cloud, by name. Left out, the provider's `cloud` (or `MTNCLOUD_CLOUD`); it must be the group's cloud."),
			"project":     defaulted("Project the nodes are built in, by name. Left out, the provider's `project` (or `MTNCLOUD_PROJECT`)."),
			"description": schema.StringAttribute{Optional: true, Description: "Description. Changes in place."},
			"labels": schema.ListAttribute{Optional: true, ElementType: types.StringType,
				Description: "Labels, on the cluster and its nodes. Change in place (the nodes keep the labels they were built with)."},
			"resource_name":      built("Name the nodes are given (the layout adds `-master`, `-worker` and a number). Left out, the cluster name."),
			"hostname":           built("Host name prefix of the nodes. Left out, the resource name."),
			"domain":             built("Network domain of the nodes, by name, for example `localdomain`."),
			"kubernetes_version": defaulted("Kubernetes only: version, as the layout lists it, for example `1.35.3`. Left out, the layout's default."),
			"pod_cidr":           defaulted("Kubernetes only: pod network. Left out, the layout's default (`10.244.0.0/16`).", cidr),
			"service_cidr":       defaulted("Kubernetes only: service network. Left out, the layout's default (`10.96.0.0/12`).", cidr),
			"registry":           built("Kubernetes only: registry the cluster pulls images from, by name (see `mtncloud_registries`). Left out, the public Docker Hub."),
			"workflow":           built("Workflow to run on each node after it is built, by name."),
			"create_user": schema.BoolAttribute{Optional: true,
				Description:   "Create your user on the nodes. Changing it builds a new cluster.",
				PlanModifiers: []planmodifier.Bool{boolReplaceIfSet{}}},
			"master": schema.SingleNestedAttribute{Required: true, Attributes: nodeAttrs(true),
				Description:   "The masters (Docker: the hosts). Changing any of it builds a new cluster.",
				PlanModifiers: []planmodifier.Object{objectReplaceIfSet{}}},
			"worker_count": schema.Int64Attribute{Optional: true, Computed: true,
				Description: "Kubernetes workers (Docker: hosts). Left out, the layout's default (3 for Kubernetes, 1 for Docker). Changing it adds " +
					"or removes workers in place (removal takes the newest). With `autoscaling`, it is only the starting count.",
				Validators: []validator.Int64{int64validator.AtLeast(1)}},
			"worker": schema.SingleNestedAttribute{Optional: true, Attributes: nodeAttrs(false),
				Description: "Worker settings that differ from the master's. Changing them applies to workers added later; existing workers keep theirs."},
			"autoscaling": schema.SingleNestedAttribute{Optional: true,
				Description: "Scale workers automatically between these counts. Changes in place; removing it turns the autoscaler off.",
				Attributes: map[string]schema.Attribute{
					"min_nodes": schema.Int64Attribute{Required: true, Description: "Fewest workers.", Validators: []validator.Int64{int64validator.AtLeast(1)}},
					"max_nodes": schema.Int64Attribute{Required: true, Description: "Most workers.", Validators: []validator.Int64{int64validator.AtLeast(1)}},
				}},
			"status":     schema.StringAttribute{Computed: true, Description: "Status, for example `ok`."},
			"api_url":    schema.StringAttribute{Computed: true, Description: "Kubernetes API address, when MTN Cloud reports one."},
			"worker_ids": schema.ListAttribute{Computed: true, ElementType: types.StringType, Description: "IDs of the workers (Docker: hosts) now in the cluster."},
		},
		Blocks: map[string]schema.Block{"timeouts": timeouts.Block(ctx, timeouts.Opts{Create: true, Update: true, Delete: true})},
	}
}

type boolReplaceIfSet struct{}

func (boolReplaceIfSet) Description(context.Context) string {
	return "Changing it builds a new cluster."
}
func (boolReplaceIfSet) MarkdownDescription(ctx context.Context) string {
	return boolReplaceIfSet{}.Description(ctx)
}
func (boolReplaceIfSet) PlanModifyBool(_ context.Context, req planmodifier.BoolRequest, resp *planmodifier.BoolResponse) {
	if !req.StateValue.IsNull() && !req.PlanValue.Equal(req.StateValue) {
		resp.RequiresReplace = true
	}
}

func (r *clusterResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	c, ok := req.ProviderData.(*client.Client)
	if !ok {
		resp.Diagnostics.AddError("Unexpected provider data", fmt.Sprintf("expected *client.Client, got %T. This is a provider bug.", req.ProviderData))
		return
	}
	r.client = c
}

func (r *clusterResource) ready(diags *diag.Diagnostics) bool {
	if r.client == nil {
		diags.AddError("MTN Cloud provider is not configured yet",
			"The provider block uses values that are only known after apply. Use values known at plan time, or apply the resources they come from first.")
		return false
	}
	return true
}

func (r *clusterResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.Plan.Raw.IsNull() || r.client == nil {
		return
	}
	d := r.client.DefaultsFor(ctx)
	resp.Diagnostics.Append(planmodifiers.FillDefault(ctx, req.Config, &resp.Plan, "group", "MTNCLOUD_GROUP", d.Group)...)
	resp.Diagnostics.Append(planmodifiers.FillDefault(ctx, req.Config, &resp.Plan, "project", "MTNCLOUD_PROJECT", d.Project)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var plan, config model
	resp.Diagnostics.Append(resp.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}
	creating := req.State.Raw.IsNull()
	var state model
	if !creating {
		resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	}
	adopting := !creating && state.Master.IsNull() // imported: what it was built with is unknown
	if config.Type.IsNull() {
		plan.Type = types.StringValue("kubernetes")
		if !creating && !state.Type.IsNull() {
			plan.Type = state.Type
		}
	}
	if !creating && !adopting {
		// Only name, description, labels, worker settings and autoscaling change in place; the
		// rest keeps its planned (state) value or replaces the cluster through its modifier.
		r.planUpdate(ctx, &plan, config, state, &resp.Diagnostics)
		if pending, d := req.Private.GetKey(ctx, scalerPending); len(pending) > 0 && !plan.Autoscaling.IsNull() {
			// The autoscaler was never configured: an update does it.
			resp.Diagnostics.Append(d...)
			plan.Status, plan.WorkerIDs = types.StringUnknown(), types.ListUnknown(types.StringType)
		}
		resp.Diagnostics.Append(resp.Plan.Set(ctx, &plan)...)
		return
	}
	if adopting {
		// Nothing is built: values left to their default keep what the import read (or nothing).
		keepUnset(&plan, config, state)
		resp.Diagnostics.Append(resp.Plan.Set(ctx, &plan)...)
		return
	}
	// A value only known after apply stays unknown: a default planned in its place would contradict
	// the configuration. The apply checks its names.
	for _, v := range []attr.Value{config.Type, config.Layout, config.Cloud, config.KubernetesVersion, config.PodCIDR,
		config.ServiceCIDR, config.WorkerCount} {
		if v.IsUnknown() {
			resp.Diagnostics.Append(resp.Plan.Set(ctx, &plan)...)
			return
		}
	}
	b := r.build(ctx, &plan, &resp.Diagnostics)
	if b == nil || resp.Diagnostics.HasError() {
		return
	}
	plan.Status, plan.APIURL, plan.WorkerIDs = types.StringUnknown(), types.StringUnknown(), types.ListUnknown(types.StringType)
	resp.Diagnostics.Append(resp.Plan.Set(ctx, &plan)...)
}

// planUpdate checks an in-place change: worker settings and autoscaling are resolved now, so a
// wrong name fails the plan, not the apply.
func (r *clusterResource) planUpdate(ctx context.Context, plan *model, config, state model, diags *diag.Diagnostics) {
	keepUnset(plan, config, state)
	r.checkAutoscaling(ctx, plan, diags)
	if (!plan.Worker.Equal(state.Worker) || !plan.WorkerCount.Equal(state.WorkerCount)) && !plan.WorkerCount.IsUnknown() {
		// Checked on a copy: building fills defaults (version, CIDRs) the cluster may not have.
		check := *plan
		if r.build(ctx, &check, diags) == nil {
			return
		}
	}
	changed := !plan.Name.Equal(state.Name) || !plan.Description.Equal(state.Description) || !plan.Labels.Equal(state.Labels) ||
		!plan.Worker.Equal(state.Worker) || !plan.WorkerCount.Equal(state.WorkerCount) || !plan.Autoscaling.Equal(state.Autoscaling)
	if changed {
		// The workers are listed again after any change, and the autoscaler may have moved them since the plan.
		plan.Status, plan.WorkerIDs = types.StringUnknown(), types.ListUnknown(types.StringType)
	} else {
		plan.Status, plan.WorkerIDs = state.Status, state.WorkerIDs
	}
	plan.APIURL = state.APIURL
}

// keepUnset gives what the configuration leaves out the value the cluster already has. A value only
// known after apply stays unknown.
func keepUnset(plan *model, config, state model) {
	for _, v := range []struct{ p, c, s *types.String }{
		{&plan.Layout, &config.Layout, &state.Layout}, {&plan.Cloud, &config.Cloud, &state.Cloud},
		{&plan.KubernetesVersion, &config.KubernetesVersion, &state.KubernetesVersion},
		{&plan.PodCIDR, &config.PodCIDR, &state.PodCIDR}, {&plan.ServiceCIDR, &config.ServiceCIDR, &state.ServiceCIDR},
	} {
		if v.c.IsNull() {
			*v.p = *v.s
		}
	}
	if config.WorkerCount.IsNull() {
		plan.WorkerCount = state.WorkerCount
	}
}

func (r *clusterResource) checkAutoscaling(ctx context.Context, m *model, diags *diag.Diagnostics) {
	if m.Autoscaling.IsNull() || m.Autoscaling.IsUnknown() {
		return
	}
	var a autoscaling
	diags.Append(m.Autoscaling.As(ctx, &a, basetypes.ObjectAsOptions{})...)
	if a.MinNodes.IsUnknown() || a.MaxNodes.IsUnknown() {
		return
	}
	if a.MinNodes.ValueInt64() > a.MaxNodes.ValueInt64() {
		diags.AddAttributeError(path.Root("autoscaling"), "Invalid autoscaling", "min_nodes can't be more than max_nodes.")
	}
	if n := m.WorkerCount; !n.IsNull() && !n.IsUnknown() && (n.ValueInt64() < a.MinNodes.ValueInt64() || n.ValueInt64() > a.MaxNodes.ValueInt64()) {
		diags.AddAttributeError(path.Root("worker_count"), "Worker count outside the autoscaling range",
			fmt.Sprintf("worker_count %d must be between min_nodes %d and max_nodes %d.", n.ValueInt64(), a.MinNodes.ValueInt64(), a.MaxNodes.ValueInt64()))
	}
}

// built is what a cluster is created with, every name resolved to its ID.
type built struct {
	body      map[string]any
	place     *client.Placement
	projectID int64
	worker    map[string]any // the server config new workers are added with
}

// fullyKnown reports whether v, and everything inside it, is known.
func fullyKnown(ctx context.Context, v attr.Value) bool {
	tv, err := v.ToTerraformValue(ctx)
	return err == nil && tv.IsFullyKnown()
}

// build resolves every name in m at plan time and fills the layout's defaults into m. It returns
// nil, with nothing wrong, when a value is only known after apply: the apply resolves it then.
func (r *clusterResource) build(ctx context.Context, m *model, diags *diag.Diagnostics) *built {
	// Optional+Computed values (type, layout, cloud, versions, worker_count) that are unknown here are
	// still to be defaulted; only the plain inputs must be known.
	for _, v := range []attr.Value{m.Name, m.Group, m.Project, m.Master, m.Worker, m.Labels, m.Description, m.Autoscaling,
		m.ResourceName, m.Hostname, m.Domain, m.Registry, m.Workflow, m.CreateUser} {
		if !fullyKnown(ctx, v) {
			if m.Layout.IsUnknown() || m.Layout.IsNull() {
				m.Layout = types.StringUnknown()
			}
			return nil
		}
	}
	kind := m.Type.ValueString()
	kube := kind == "kubernetes"
	fail := func(a string, err error) {
		diags.AddAttributeError(path.Root(a), "Invalid "+strings.ReplaceAll(a, "_", " "), err.Error())
	}
	if !kube {
		for a, v := range map[string]types.String{"kubernetes_version": m.KubernetesVersion, "pod_cidr": m.PodCIDR, "service_cidr": m.ServiceCIDR, "registry": m.Registry} {
			if !v.IsNull() && v.ValueString() != "" && !v.IsUnknown() {
				fail(a, fmt.Errorf("%s is for Kubernetes clusters only", a))
			}
		}
	}
	layoutName := m.Layout.ValueString()
	if m.Layout.IsNull() || m.Layout.IsUnknown() {
		layoutName = defaultLayouts[kind]
	}
	layout, err := r.client.ClusterLayoutByName(ctx, typeCodes[kind], layoutName)
	if err != nil {
		fail("layout", err)
		return nil
	}
	if m.Layout.IsNull() || m.Layout.IsUnknown() {
		m.Layout = types.StringValue(layout.Name) // set by the user, it is kept as typed
	}
	place, err := r.client.PlacementForGroup(ctx, m.Group.ValueString())
	if err != nil {
		fail("group", err)
		return nil
	}
	if !m.Cloud.IsNull() && !m.Cloud.IsUnknown() && !strings.EqualFold(m.Cloud.ValueString(), place.CloudName) {
		fail("cloud", fmt.Errorf("group %q is on cloud %q, not %q", place.GroupName, place.CloudName, m.Cloud.ValueString()))
		return nil
	}
	if m.Cloud.IsNull() || m.Cloud.IsUnknown() {
		m.Cloud = types.StringValue(place.CloudName)
	}
	pr, err := r.client.ProjectByNameAt(ctx, place, m.Project.ValueString())
	if err != nil {
		fail("project", err)
		return nil
	}
	b := &built{place: place, projectID: pr.ID}
	if m.WorkerCount.IsNull() || m.WorkerCount.IsUnknown() {
		m.WorkerCount = types.Int64Value(max(layout.Workers, 1))
	}
	r.checkAutoscaling(ctx, m, diags)

	var master, worker node
	diags.Append(m.Master.As(ctx, &master, basetypes.ObjectAsOptions{})...)
	if !m.Worker.IsNull() {
		diags.Append(m.Worker.As(ctx, &worker, basetypes.ObjectAsOptions{})...)
	}
	if diags.HasError() {
		return nil
	}
	server := r.nodeConfig(ctx, b, layout, "master", master, diags)
	b.worker = r.nodeConfig(ctx, b, layout, "worker", inherit(worker, master), diags)
	if diags.HasError() {
		return nil
	}
	count := m.WorkerCount.ValueInt64()
	config := server["config"].(map[string]any)
	config["nodeCount"] = count
	server["nodeCount"] = count
	server["visibility"] = "private"
	if v := m.ResourceName.ValueString(); v != "" {
		server["name"] = v
	}
	if v := m.Hostname.ValueString(); v != "" {
		server["hostname"] = v
	}
	if v := m.Domain.ValueString(); v != "" {
		dom, err := r.client.NetworkDomainByName(ctx, v)
		if err != nil {
			fail("domain", err)
		} else {
			server["networkDomain"] = map[string]any{"id": dom.ID}
		}
	}
	var labels []string
	diags.Append(m.Labels.ElementsAs(ctx, &labels, false)...)
	if len(labels) > 0 {
		server["labels"] = labels
	}
	if !m.CreateUser.IsNull() {
		config["createUser"] = m.CreateUser.ValueBool()
	}
	if kube {
		custom := map[string]any{}
		for _, o := range []struct {
			v   *types.String
			key string
		}{{&m.KubernetesVersion, "k8s_version"}, {&m.PodCIDR, "podCidr"}, {&m.ServiceCIDR, "serviceCidr"}} {
			f, ok := layout.Options[o.key]
			if !ok {
				if !o.v.IsNull() && !o.v.IsUnknown() {
					fail(attrOf(o.key), fmt.Errorf("layout %q has no %s input", layout.Name, attrOf(o.key)))
				}
				*o.v = types.StringNull()
				continue
			}
			want, given := o.v.ValueString(), !o.v.IsNull() && !o.v.IsUnknown()
			if !given {
				want = layout.Defaults[o.key]
			}
			if f.Type == "select" {
				c, err := r.client.FormChoice(ctx, f, strings.ReplaceAll(attrOf(o.key), "_", " "), want)
				if err != nil {
					fail(attrOf(o.key), err)
					continue
				}
				custom[o.key] = c.Value
				if !given {
					*o.v = types.StringValue(c.Name) // set by the user (a name or a value), it is kept as typed
				}
				continue
			}
			custom[o.key] = want
			*o.v = types.StringValue(want)
		}
		config["customOptions"] = custom
		// The platform's own MKS clients send the networks at config level too.
		config["podCidr"], config["serviceCidr"] = custom["podCidr"], custom["serviceCidr"]
		if v := m.Registry.ValueString(); v != "" {
			reg, err := r.client.RegistryByName(ctx, place, v)
			if err != nil {
				fail("registry", err)
			} else if reg.ID != 0 {
				config["defaultRepoAccount"] = reg.ID
			}
		}
	} else {
		m.KubernetesVersion, m.PodCIDR, m.ServiceCIDR = types.StringNull(), types.StringNull(), types.StringNull()
	}
	body := map[string]any{
		"name": m.Name.ValueString(), "type": typeCodes[kind], "layout": map[string]any{"id": layout.ID},
		"group": map[string]any{"id": place.GroupID}, "cloud": map[string]any{"id": place.CloudID},
		"server": server,
	}
	if v := m.Description.ValueString(); v != "" {
		body["description"] = v
	}
	if len(labels) > 0 {
		body["labels"] = labels
	}
	if v := m.Workflow.ValueString(); v != "" {
		wf, err := r.client.WorkflowByName(ctx, v)
		if err != nil {
			fail("workflow", err)
		} else {
			body["taskSetId"] = wf.ID
		}
	}
	if !m.Worker.IsNull() {
		w := map[string]any{}
		for k, v := range b.worker {
			if k != "nodeCount" {
				w[k] = v
			}
		}
		body["worker"] = w
	}
	if diags.HasError() {
		return nil
	}
	b.body = body
	return b
}

func attrOf(key string) string {
	return map[string]string{"k8s_version": "kubernetes_version", "podCidr": "pod_cidr", "serviceCidr": "service_cidr"}[key]
}

// inherit fills a worker's unset fields from the master.
func inherit(w, m node) node {
	if w.Plan.IsNull() {
		w.Plan = m.Plan
	}
	if w.RootVolumeSize.IsNull() {
		w.RootVolumeSize, w.Datastore = m.RootVolumeSize, m.Datastore
	}
	if w.DataVolumes == nil {
		w.DataVolumes = m.DataVolumes
	}
	if w.Networks == nil {
		w.Networks = m.Networks
	}
	if w.SecurityGroups == nil {
		w.SecurityGroups = m.SecurityGroups
	}
	if w.FloatingIPPool.IsNull() {
		w.FloatingIPPool = m.FloatingIPPool
	}
	return w
}

// nodeConfig builds the server part shared by masters and workers: plan, disks, networks,
// security groups, floating IP and project. what is "master" or "worker", for errors.
func (r *clusterResource) nodeConfig(ctx context.Context, b *built, layout client.ClusterLayout, what string, n node, diags *diag.Diagnostics) map[string]any {
	at := func(a string) path.Path { return path.Root(what).AtName(a) }
	fail := func(a string, err error) {
		diags.AddAttributeError(at(a), "Invalid "+what+" "+strings.ReplaceAll(a, "_", " "), err.Error())
	}
	config := map[string]any{"resourcePoolId": b.projectID}
	out := map[string]any{"config": config}
	if pl, err := r.client.ClusterPlanByName(ctx, b.place, layout, b.projectID, n.Plan.ValueString()); err != nil {
		fail("plan", err)
	} else {
		out["plan"] = map[string]any{"id": pl.ID}
	}
	vols := []any{}
	all := append([]volume{{Size: n.RootVolumeSize, Datastore: n.Datastore}}, n.DataVolumes...)
	for i, v := range all {
		vol := map[string]any{"name": "root", "rootVolume": i == 0, "size": v.Size.ValueInt64()}
		if i > 0 {
			vol["name"] = "data-" + strconv.Itoa(i)
		}
		if ds := v.Datastore.ValueString(); ds != "" {
			o, err := r.client.DatastoreByName(ctx, b.place, b.projectID, ds)
			if err != nil {
				fail("datastore", err)
				continue
			}
			vol["storageType"] = o.ID
		}
		vols = append(vols, vol)
	}
	out["volumes"] = vols
	nets := []any{}
	for i, name := range n.Networks {
		nw, err := r.client.FindNetwork(ctx, name, b.projectID)
		if err == nil && nw == nil {
			err = fmt.Errorf("no network named %q in the project", name)
		}
		if err != nil {
			fail("networks", err)
			continue
		}
		nets = append(nets, map[string]any{"primaryInterface": i == 0, "network": map[string]any{"id": "network-" + strconv.FormatInt(nw.ID, 10)}})
	}
	out["networkInterfaces"] = nets
	sgs := []any{}
	for _, name := range n.SecurityGroups {
		sg, err := r.client.SecurityGroupByName(ctx, b.place, b.projectID, name)
		if err != nil {
			fail("security_groups", err)
			continue
		}
		sgs = append(sgs, map[string]any{"id": sg.ID})
	}
	if len(sgs) > 0 {
		out["securityGroups"] = sgs
	}
	if v := n.FloatingIPPool.ValueString(); v != "" {
		pool, err := r.client.ClusterFloatingIPPoolByName(ctx, b.place, b.projectID, v)
		if err != nil {
			fail("floating_ip_pool", err)
		} else {
			config["osExternalNetworkId"] = pool
		}
	}
	return out
}

func (r *clusterResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	if !r.ready(&resp.Diagnostics) {
		return
	}
	var m model
	resp.Diagnostics.Append(req.Plan.Get(ctx, &m)...)
	timeout, d := m.Timeouts.Create(ctx, createTimeout)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	b := r.build(ctx, &m, &resp.Diagnostics)
	if b == nil {
		if !resp.Diagnostics.HasError() {
			resp.Diagnostics.AddError("Unable to build the cluster", "A value was still unknown at apply. This is a provider bug.")
		}
		return
	}
	// Names aren't unique in MTN Cloud: an existing cluster is imported, never built twice.
	same, err := r.client.FindClustersNamed(ctx, m.Name.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Unable to check for an existing cluster", err.Error())
		return
	}
	if len(same) > 0 {
		resp.Diagnostics.AddError("Cluster already exists",
			fmt.Sprintf("A cluster named %q already exists (ID %d). Import it or choose another name.", m.Name.ValueString(), same[0].ID))
		return
	}
	id, err := r.client.CreateCluster(ctx, b.body)
	if err != nil {
		resp.Diagnostics.AddError("Unable to create the cluster", err.Error())
		return
	}
	// Saved before the wait: a timeout never leads to a second cluster.
	m.ID, m.Status, m.APIURL, m.WorkerIDs = types.StringValue(strconv.FormatInt(id, 10)), types.StringValue("provisioning"),
		types.StringNull(), types.ListValueMust(types.StringType, nil)
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
	c, err := r.client.WaitForCluster(ctx, id)
	if err != nil {
		// A warning, not an error: an error would taint the cluster, and the next apply would rebuild it.
		resp.Diagnostics.AddWarning("Cluster not ready yet", fmt.Sprintf("Cluster %d was created but isn't ready: %s. A later refresh reads its status.", id, err))
		if !m.Autoscaling.IsNull() {
			resp.Diagnostics.Append(resp.Private.SetKey(ctx, scalerPending, []byte("true"))...)
		}
		return
	}
	if client.ClusterFailed(c.Status) {
		resp.Diagnostics.AddError("Cluster failed", fmt.Sprintf("Cluster %d ended with %q: %s. The next apply replaces it.", id, c.Status, c.StatusMessage))
		return
	}
	if err := r.applyAutoscaling(ctx, id, m, &resp.Diagnostics); err != nil {
		// A warning: an error would taint a cluster that was built fine. Left out of state, the
		// autoscaler is configured by the next apply.
		resp.Diagnostics.AddWarning("Autoscaler not configured",
			fmt.Sprintf("Cluster %d was built, but configuring its autoscaler failed: %s. The next apply tries again.", id, err))
		resp.Diagnostics.Append(resp.Private.SetKey(ctx, scalerPending, []byte("true"))...)
	}
	r.refresh(ctx, &m, c, false, &resp.Diagnostics)
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

func (r *clusterResource) applyAutoscaling(ctx context.Context, id int64, m model, diags *diag.Diagnostics) error {
	if m.Autoscaling.IsNull() {
		return nil
	}
	var a autoscaling
	diags.Append(m.Autoscaling.As(ctx, &a, basetypes.ObjectAsOptions{})...)
	return r.client.ConfigureAutoscaler(ctx, id, true, a.MinNodes.ValueInt64(), a.MaxNodes.ValueInt64())
}

// refresh copies what MTN Cloud reports. What the nodes were built with isn't readable, so it stays.
// readCount takes worker_count from the workers listed. Only a plain refresh does: after a create
// or an update the plan's count stands, even while workers are still coming.
func (r *clusterResource) refresh(ctx context.Context, m *model, c *client.Cluster, readCount bool, diags *diag.Diagnostics) {
	m.ID, m.Name, m.Status = types.StringValue(strconv.FormatInt(c.ID, 10)), types.StringValue(c.Name), types.StringValue(c.Status)
	m.APIURL = types.StringNull()
	if c.ServiceURL != "" {
		m.APIURL = types.StringValue(c.ServiceURL)
	}
	if c.Description != "" || !m.Description.IsNull() {
		m.Description = types.StringValue(c.Description)
	}
	if len(c.Labels) > 0 || len(m.Labels.Elements()) > 0 {
		l, d := types.ListValueFrom(ctx, types.StringType, c.Labels)
		diags.Append(d...)
		m.Labels = l
	}
	for k, code := range typeCodes {
		if code == c.TypeCode {
			m.Type = types.StringValue(k)
		}
	}
	if layouts, err := r.client.ClusterLayouts(ctx); err == nil {
		for _, l := range layouts {
			if l.ID == c.LayoutID && !strings.EqualFold(m.Layout.ValueString(), l.Name) {
				m.Layout = types.StringValue(l.Name)
			}
		}
	}
	workers, err := r.client.ClusterWorkers(ctx, c.ID)
	if err != nil {
		diags.AddWarning("Unable to list the cluster's workers", err.Error())
		if m.WorkerIDs.IsUnknown() {
			m.WorkerIDs = types.ListNull(types.StringType)
		}
		return
	}
	ids := make([]string, 0, len(workers))
	for _, w := range workers {
		ids = append(ids, strconv.FormatInt(w.ID, 10))
	}
	l, d := types.ListValueFrom(ctx, types.StringType, ids)
	diags.Append(d...)
	m.WorkerIDs = l
	// The autoscaler moves the count: then worker_count is only where it started. A cluster still
	// building lists only some of its workers, so its count isn't read either.
	settled := map[string]bool{"ok": true, "warning": true}[strings.ToLower(c.Status)] || client.ClusterFailed(c.Status)
	if m.WorkerCount.IsNull() || (readCount && settled && m.Autoscaling.IsNull()) {
		m.WorkerCount = types.Int64Value(int64(len(workers)))
	}
}

func (r *clusterResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	if !r.ready(&resp.Diagnostics) {
		return
	}
	var m model
	resp.Diagnostics.Append(req.State.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	id, err := strconv.ParseInt(m.ID.ValueString(), 10, 64)
	if err != nil {
		resp.Diagnostics.AddError("Invalid cluster ID", fmt.Sprintf("%q is not a cluster ID.", m.ID.ValueString()))
		return
	}
	c, err := r.client.GetCluster(ctx, id)
	if client.IsNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Unable to read the cluster", err.Error())
		return
	}
	if _, ok := map[string]bool{"kubernetes-cluster": true, "docker-cluster": true}[c.TypeCode]; !ok && c.TypeCode != "" {
		resp.Diagnostics.AddError("Unsupported cluster type", fmt.Sprintf("Cluster %d is a %q cluster; only Kubernetes and Docker clusters can be managed.", id, c.TypeCode))
		return
	}
	if m.Timeouts.IsNull() {
		m.Timeouts = timeouts.Value{Object: types.ObjectNull(map[string]attr.Type{
			"create": types.StringType, "update": types.StringType, "delete": types.StringType})}
	}
	r.refresh(ctx, &m, c, true, &resp.Diagnostics)
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

func (r *clusterResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	if !r.ready(&resp.Diagnostics) {
		return
	}
	var plan, state model
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	timeout, d := plan.Timeouts.Update(ctx, changeTimeout)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	id, err := strconv.ParseInt(state.ID.ValueString(), 10, 64)
	if err != nil {
		resp.Diagnostics.AddError("Invalid cluster ID", fmt.Sprintf("%q is not a cluster ID.", state.ID.ValueString()))
		return
	}
	if !plan.Name.Equal(state.Name) || !plan.Description.Equal(state.Description) || !plan.Labels.Equal(state.Labels) {
		var labels []string
		resp.Diagnostics.Append(plan.Labels.ElementsAs(ctx, &labels, false)...)
		if err := r.client.UpdateCluster(ctx, id, plan.Name.ValueString(), plan.Description.ValueString(), labels); err != nil {
			resp.Diagnostics.AddError("Unable to update the cluster", err.Error())
			return
		}
	}
	scalerOff := !state.Autoscaling.IsNull() && plan.Autoscaling.IsNull()
	if scalerOff {
		if err := r.client.ConfigureAutoscaler(ctx, id, false, 0, 0); err != nil {
			resp.Diagnostics.AddError("Unable to turn the autoscaler off", err.Error())
			return
		}
	}
	// With the autoscaler off, worker_count is the count again, wherever the autoscaler left it.
	if !plan.WorkerCount.Equal(state.WorkerCount) || scalerOff {
		r.scale(ctx, id, plan, &resp.Diagnostics)
		if resp.Diagnostics.HasError() {
			return
		}
	}
	pending, d := req.Private.GetKey(ctx, scalerPending)
	resp.Diagnostics.Append(d...)
	if !plan.Autoscaling.IsNull() && (!plan.Autoscaling.Equal(state.Autoscaling) || len(pending) > 0) {
		if err := r.applyAutoscaling(ctx, id, plan, &resp.Diagnostics); err != nil {
			resp.Diagnostics.AddError("Unable to configure the autoscaler", err.Error())
			return
		}
	}
	if len(pending) > 0 {
		resp.Diagnostics.Append(resp.Private.SetKey(ctx, scalerPending, nil)...)
	}
	c, err := r.client.GetCluster(ctx, id)
	if err != nil {
		resp.Diagnostics.AddError("Unable to read the cluster", err.Error())
		return
	}
	plan.ID = state.ID
	r.refresh(ctx, &plan, c, false, &resp.Diagnostics)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// scale adds or removes workers to reach worker_count. New workers use the worker settings in plan.
func (r *clusterResource) scale(ctx context.Context, id int64, plan model, diags *diag.Diagnostics) {
	workers, err := r.client.ClusterWorkers(ctx, id)
	if err != nil {
		diags.AddError("Unable to list the cluster's workers", err.Error())
		return
	}
	want := int(plan.WorkerCount.ValueInt64())
	switch {
	case want > len(workers):
		b := r.build(ctx, &plan, diags) // a copy: building fills defaults the cluster may not have
		if b == nil {
			if !diags.HasError() {
				diags.AddError("Unable to add workers", "A worker setting was still unknown at apply. This is a provider bug.")
			}
			return
		}
		body := b.worker
		body["nodeCount"] = want - len(workers)
		body["cloud"] = map[string]any{"id": b.place.CloudID}
		// The add-worker reference names config.resourcePool; the create form uses resourcePoolId. Both are sent.
		body["config"].(map[string]any)["resourcePool"] = map[string]any{"id": b.projectID}
		if err := r.client.AddClusterWorkers(ctx, id, body); err != nil {
			diags.AddError("Unable to add workers", err.Error())
			return
		}
	case want < len(workers):
		slices.SortFunc(workers, func(a, b client.ClusterNode) int { return int(b.ID - a.ID) })
		for _, w := range workers[:len(workers)-want] {
			if err := r.client.DeleteClusterWorker(ctx, id, w.ID); err != nil {
				diags.AddError("Unable to remove worker "+w.Name, err.Error())
				return
			}
		}
	default:
		return
	}
	// The cluster can stay "ok" while workers come and go: wait for the list itself.
	if err := r.client.WaitForClusterWorkers(ctx, id, want); err != nil {
		diags.AddWarning("Workers still changing",
			fmt.Sprintf("The cluster didn't reach %d workers before the wait ended: %s. A later refresh reads the count.", want, err))
	}
}

func (r *clusterResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	if !r.ready(&resp.Diagnostics) {
		return
	}
	var m model
	resp.Diagnostics.Append(req.State.Get(ctx, &m)...)
	timeout, d := m.Timeouts.Delete(ctx, changeTimeout)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	id, err := strconv.ParseInt(m.ID.ValueString(), 10, 64)
	if err != nil {
		resp.Diagnostics.AddError("Invalid cluster ID", fmt.Sprintf("%q is not a cluster ID; nothing was deleted.", m.ID.ValueString()))
		return
	}
	if err := r.client.DeleteCluster(ctx, id); err != nil {
		resp.Diagnostics.AddError("Unable to delete the cluster", err.Error())
		return
	}
	if err := r.client.WaitForClusterGone(ctx, id); err != nil {
		resp.Diagnostics.AddError("Cluster still being deleted", fmt.Sprintf("Cluster %d was asked to delete but is still there: %s.", id, err))
	}
}

func (r *clusterResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	if _, err := strconv.ParseInt(req.ID, 10, 64); err != nil {
		resp.Diagnostics.AddError("Invalid import ID", fmt.Sprintf("Import a cluster by its numeric ID. Got %q.", req.ID))
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
}
