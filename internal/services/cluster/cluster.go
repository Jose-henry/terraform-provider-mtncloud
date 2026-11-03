// Package cluster holds mtncloud_managed_kubernetes_cluster and mtncloud_docker_cluster: clusters
// built from MTN's own cluster layouts, as MTN's `Manage Kubernetes/CreateCluster.sh` builds them.
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
	return []func() resource.Resource{
		func() resource.Resource { return &clusterResource{kind: kube} },
		func() resource.Resource { return &clusterResource{kind: docker} },
	}
}

type clusterResource struct {
	client *client.Client
	kind   string // kube or docker
}

type model struct {
	ID                types.String   `tfsdk:"id"`
	Name              types.String   `tfsdk:"name"`
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
	DataVolumes    []volume     `tfsdk:"data_volume"`
	Networks       []string     `tfsdk:"networks"`
	SecurityGroups []string     `tfsdk:"security_groups"`
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
	if r.kind == kube {
		resp.TypeName = req.ProviderTypeName + "_managed_kubernetes_cluster"
		return
	}
	resp.TypeName = req.ProviderTypeName + "_docker_cluster"
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
	return nodeAttrsFor(master, "master")
}

// rootGB is the fixed root disk of every node, Kubernetes and Docker alike (owner, 2026-11-02:
// 100 GB, CreateCluster.sh moves from 50 to match); what a user sizes are the data disks.
const rootGB = 100

// nodeAttrsFor is a node block's attributes; who names the nodes ("master" or "host"; a worker
// block is always "worker").
func nodeAttrsFor(master bool, who string) map[string]schema.Attribute {
	if !master {
		who = "worker"
	}
	inherit := ""
	if !master {
		inherit = " Left out, the master's is used."
	}
	return map[string]schema.Attribute{
		"plan": schema.StringAttribute{Required: master, Optional: !master,
			Description: "Plan (size) of each " + who + " node, by name, for example `G4S16`." + inherit},
		"data_volume": schema.ListNestedAttribute{Required: master, Optional: !master,
			Description: "Data disks, at least one: container data lives on them. The root disk is always 100 GB." + inherit,
			Validators:  []validator.List{listvalidator.SizeAtLeast(1)},
			NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
				"size": schema.Int64Attribute{Required: true, Description: "Size in GB.", Validators: []validator.Int64{int64validator.AtLeast(1)}},
				"datastore": schema.StringAttribute{Optional: true,
					Description: "Datastore of the disk, by name, for example `ENCR_AT_REST` (see `mtncloud_datastores`). Left out, the project's default."},
			}}},
		"networks": schema.ListAttribute{Required: master, Optional: !master, ElementType: types.StringType,
			Description: "MTN Networks of the project, by name; the first is the primary interface." + inherit,
			Validators:  []validator.List{listvalidator.SizeAtLeast(1)}},
		"security_groups": schema.ListAttribute{Optional: true, ElementType: types.StringType,
			Description: "Security groups, by name, in addition to the project's default group, which is always applied. " +
				"Listing the default too changes nothing." + inherit},
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
	nodes := "nodes"
	if r.kind == docker {
		nodes = "hosts"
	}
	attrs := map[string]schema.Attribute{
		"id":    schema.StringAttribute{Computed: true, Description: "Cluster ID.", PlanModifiers: keep},
		"name":  schema.StringAttribute{Required: true, Description: "Cluster name. Changing it renames the cluster in place."},
		"group": defaulted("Group, by name. Left out, the provider's `group` (or `MTNCLOUD_GROUP`)."),
		"cloud": defaulted("Cloud, by name. Left out, the provider's `cloud` (or `MTNCLOUD_CLOUD`); it must be the group's cloud."),
		"project": defaulted("Project the " + nodes + " are built in, by name. Left out, the provider's `project` (or " +
			"`MTNCLOUD_PROJECT`)."),
		"description": schema.StringAttribute{Optional: true, Description: "Description. Changes in place.",
			Validators: []validator.String{stringvalidator.LengthAtLeast(1)}},
		"labels": schema.ListAttribute{Optional: true, ElementType: types.StringType,
			Description: "Labels, on the cluster and its " + nodes + ". Change in place (the " + nodes + " keep the labels they were built with)."},
		"resource_name": built("Name the " + nodes + " are given (MTN adds the role and a number). Left out, the cluster name."),
		"hostname":      built("Host name prefix of the " + nodes + ". Left out, the resource name."),
		"domain":        built("Network domain of the " + nodes + ", by name, for example `localdomain`."),
		"workflow":      built("Workflow to run on each node after it is built, by name."),
		"create_user": schema.BoolAttribute{Optional: true,
			Description:   "Create your user on the " + nodes + ". Left out, `true`, as MTN's own cluster order does. Changing it builds a new cluster.",
			PlanModifiers: []planmodifier.Bool{boolReplaceIfSet{}}},
		"status": schema.StringAttribute{Computed: true, Description: "Status, for example `ok`."},
	}
	blocks := map[string]schema.Block{"timeouts": timeouts.Block(ctx, timeouts.Opts{Create: true, Update: true, Delete: true})}
	if r.kind == docker {
		attrs["host"] = schema.SingleNestedAttribute{Required: true, Attributes: nodeAttrsFor(true, "host"),
			Description:   "The Docker hosts. Changing it builds a new cluster.",
			PlanModifiers: []planmodifier.Object{objectReplaceIfSet{}}}
		attrs["host_count"] = schema.Int64Attribute{Optional: true, Computed: true,
			Description: "Number of hosts. Left out, the layout's default (1). Changing it adds or removes hosts in place (removal takes the newest).",
			Validators:  []validator.Int64{int64validator.AtLeast(1)}}
		attrs["host_ids"] = schema.ListAttribute{Computed: true, ElementType: types.StringType, Description: "IDs of the hosts now in the cluster."}
		resp.Schema = schema.Schema{
			Description: "A Docker cluster, built from MTN's Docker layout (`MTN Docker Cluster`): a set of Docker hosts " +
				"built from `host`. The hosts' security groups must allow what your containers serve.",
			Attributes: attrs,
			Blocks:     blocks,
		}
		return
	}
	cidr := validators.IPv4CIDR()
	attrs["layout"] = defaulted("`single` (one master, the default), `ha3` (three masters) or `ha5` (five masters): MTN's "+
		"layouts `MTN Kubernetes Single-Master`, `MTN Kubernetes HA (3)` and `MTN Kubernetes HA (5)`. Changing it builds a new cluster.",
		stringvalidator.OneOf("single", "ha3", "ha5"))
	attrs["kubernetes_version"] = defaulted("Kubernetes version, as the layout lists it, for example `1.35.3`. Left out, the layout's default.")
	attrs["pod_cidr"] = defaulted("Pod network. Left out, the layout's default (`10.244.0.0/16`).", cidr)
	attrs["service_cidr"] = defaulted("Service network. Left out, the layout's default (`10.96.0.0/12`).", cidr)
	attrs["registry"] = built("Registry the cluster pulls images from, by name (see `mtncloud_registries`). Left out, the public Docker Hub.")
	attrs["master"] = schema.SingleNestedAttribute{Required: true, Attributes: nodeAttrs(true),
		Description:   "The masters. Changing any of it builds a new cluster.",
		PlanModifiers: []planmodifier.Object{objectReplaceIfSet{}}}
	attrs["worker_count"] = schema.Int64Attribute{Optional: true, Computed: true,
		Description: "Number of workers. Left out, 3, as MTN's own cluster order does. Changing it adds or removes workers in place " +
			"(removal takes the newest). With `autoscaling`, it is only the starting count.",
		Validators: []validator.Int64{int64validator.AtLeast(1)}}
	attrs["worker"] = schema.SingleNestedAttribute{Optional: true, Attributes: nodeAttrs(false),
		Description: "Worker settings that differ from the master's: plan, data disks, networks, security groups. " +
			"Changing them applies to workers added later; existing workers keep theirs."}
	attrs["autoscaling"] = schema.SingleNestedAttribute{Optional: true,
		Description: "Scale workers automatically between these counts. Changes in place. Left out, the autoscaler is held at " +
			"`worker_count`, as MTN's own cluster order does (a cluster built with it off never finishes its workers).",
		Attributes: map[string]schema.Attribute{
			"min_nodes": schema.Int64Attribute{Required: true, Description: "Fewest workers.", Validators: []validator.Int64{int64validator.AtLeast(1)}},
			"max_nodes": schema.Int64Attribute{Required: true, Description: "Most workers.", Validators: []validator.Int64{int64validator.AtLeast(1)}},
		}}
	attrs["api_url"] = schema.StringAttribute{Computed: true, Description: "Kubernetes API address, when MTN Cloud reports one."}
	attrs["worker_ids"] = schema.ListAttribute{Computed: true, ElementType: types.StringType, Description: "IDs of the workers now in the cluster."}
	resp.Schema = schema.Schema{
		Description: "A managed Kubernetes cluster, built from MTN's Kubernetes layouts as MTN's own cluster order builds it: " +
			"the masters from `master`, the workers from `worker` where it is set and the master's settings otherwise, every node " +
			"with a fixed 100 GB root disk and no public IP, and the `MK8S` label. The security groups must allow the Kubernetes API (TCP 6443) " +
			"and the kubelet (TCP 10250) between the nodes. Only a Customer Admin or Power User login can run kubectl on it from the console.",
		Attributes: attrs,
		Blocks:     blocks,
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
	resp.Diagnostics.Append(r.get(ctx, resp.Plan, &plan)...)
	resp.Diagnostics.Append(r.get(ctx, req.Config, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}
	creating := req.State.Raw.IsNull()
	var state model
	if !creating {
		resp.Diagnostics.Append(r.get(ctx, req.State, &state)...)
	}
	adopting := !creating && state.Master.IsNull() // imported: what it was built with is unknown
	if !creating && !adopting {
		// Only name, description, labels, worker settings and autoscaling change in place; the
		// rest keeps its planned (state) value or replaces the cluster through its modifier.
		r.planUpdate(ctx, &plan, config, state, &resp.Diagnostics)
		if pending, d := req.Private.GetKey(ctx, scalerPending); len(pending) > 0 && !plan.Autoscaling.IsNull() {
			// The autoscaler was never configured: an update does it.
			resp.Diagnostics.Append(d...)
			plan.Status, plan.WorkerIDs = types.StringUnknown(), types.ListUnknown(types.StringType)
		}
		resp.Diagnostics.Append(r.set(ctx, &resp.Plan, &plan)...)
		return
	}
	if adopting {
		// Nothing is built: values left to their default keep what the import read (or nothing).
		keepUnset(&plan, config, state)
		resp.Diagnostics.Append(r.set(ctx, &resp.Plan, &plan)...)
		return
	}
	// A value only known after apply stays unknown: a default planned in its place would contradict
	// the configuration. The apply checks its names.
	for _, v := range []attr.Value{config.Layout, config.Cloud, config.KubernetesVersion, config.PodCIDR,
		config.ServiceCIDR, config.WorkerCount} {
		if v.IsUnknown() {
			resp.Diagnostics.Append(r.set(ctx, &resp.Plan, &plan)...)
			return
		}
	}
	b := r.build(ctx, &plan, &resp.Diagnostics)
	if b == nil || resp.Diagnostics.HasError() {
		return
	}
	plan.Status, plan.WorkerIDs = types.StringUnknown(), types.ListUnknown(types.StringType)
	if r.kind == kube {
		plan.APIURL = types.StringUnknown()
	}
	resp.Diagnostics.Append(r.set(ctx, &resp.Plan, &plan)...)
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
		r.addError(diags, "autoscaling", "Invalid autoscaling", "min_nodes can't be more than max_nodes.")
	}
	if n := m.WorkerCount; !n.IsNull() && !n.IsUnknown() && (n.ValueInt64() < a.MinNodes.ValueInt64() || n.ValueInt64() > a.MaxNodes.ValueInt64()) {
		r.addError(diags, "worker_count", "Worker count outside the autoscaling range",
			fmt.Sprintf("worker_count %d must be between min_nodes %d and max_nodes %d.", n.ValueInt64(), a.MinNodes.ValueInt64(), a.MaxNodes.ValueInt64()))
	}
}

// built is what a cluster is created with, every name resolved to its ID.
type built struct {
	body      map[string]any
	place     *client.Placement
	projectID int64
	worker    map[string]any // the server config new workers are added with
	addType   int64          // the server type new workers (Docker: hosts) are added as
	layout    string
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
			if r.kind == kube && (m.Layout.IsUnknown() || m.Layout.IsNull()) {
				m.Layout = types.StringUnknown()
			}
			return nil
		}
	}
	isKube := r.kind == kube
	fail := func(a string, err error) {
		r.addError(diags, a, "Invalid "+strings.ReplaceAll(r.attrName(a), "_", " "), err.Error())
	}
	want := dockerLayout
	if isKube {
		if m.Layout.IsNull() || m.Layout.IsUnknown() {
			m.Layout = types.StringValue("single")
		}
		var ok bool
		if want, ok = kubeLayouts[m.Layout.ValueString()]; !ok {
			want = mtnLayout{name: m.Layout.ValueString()} // imported from a layout other than MTN's three
		}
	}
	layout, err := r.client.ClusterLayoutByCode(ctx, typeCodes[r.kind], want.code, want.name)
	if err != nil {
		fail("layout", err)
		return nil
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
	b := &built{place: place, projectID: pr.ID, addType: layout.AddServerType, layout: layout.Name}
	if m.WorkerCount.IsNull() || m.WorkerCount.IsUnknown() {
		// Kubernetes: 3, MTN's order's default whatever the layout says (CreateCluster.sh workerCount).
		m.WorkerCount = types.Int64Value(max(layout.Workers, 1))
		if isKube {
			m.WorkerCount = types.Int64Value(3)
		}
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
	// The worker (Docker: host) count goes in the server part for every cluster type, as the
	// platform's own CLI sends it (clusters.rb add: server.nodeCount and server.config.nodeCount).
	// The worker block's nodeCount alone is ignored: QA cluster 146 (2026-11-03) asked for 1 worker
	// and got the layout's 3, with the autoscaler held at 1.
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
	if isKube {
		labels = withMK8S(labels)
	}
	if len(labels) > 0 {
		server["labels"] = labels
	}
	// MTN's order always creates the user, with the agent (CreateCluster.sh).
	createUser := m.CreateUser.IsNull() || m.CreateUser.ValueBool()
	for _, c := range []map[string]any{config, b.worker["config"].(map[string]any)} {
		c["createUser"], c["noAgent"] = createUser, false
	}
	top := map[string]any{}
	if isKube {
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
		repo := ""
		if v := m.Registry.ValueString(); v != "" {
			reg, err := r.client.RegistryByName(ctx, place, v)
			if err != nil {
				fail("registry", err)
			} else if reg.ID != 0 {
				repo = strconv.FormatInt(reg.ID, 10)
			}
		}
		// The autoscaler is always sent on: pinned to the worker count when autoscaling is left out.
		// A cluster built with it off never finishes its workers (CreateCluster.sh, clusters 131 and
		// 139). The values are strings, as the console's wizard sends them.
		lo, hi := scalerRange(ctx, *m, diags)
		config["autoScalerEnabled"], config["minNodes"], config["maxNodes"] = "on", lo, hi
		top = map[string]any{"workerCount": count, "defaultRepoAccount": repo, "k8s_version": custom["k8s_version"],
			"podCidr": custom["podCidr"], "serviceCidr": custom["serviceCidr"], "initConfig": "",
			"autoScalerEnabled": "on", "minNodes": lo, "maxNodes": hi}
	}
	body := map[string]any{
		"name": m.Name.ValueString(), "type": typeCodes[r.kind], "layout": map[string]any{"id": layout.ID},
		"group": map[string]any{"id": place.GroupID}, "cloud": map[string]any{"id": place.CloudID},
		"visibility": "private", "server": server,
	}
	if len(top) > 0 {
		body["config"] = top
	}
	switch v := m.Description.ValueString(); {
	case v != "":
		body["description"] = v
	case isKube:
		body["description"] = mk8sDescription
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
	if isKube {
		// Always sent, with its own security groups: with them on the server only, the workers get
		// the project's default group (CreateCluster.sh, verified on cluster 131).
		w := map[string]any{"nodeCount": count}
		for k, v := range b.worker {
			w[k] = v
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
	if w.DataVolumes == nil {
		w.DataVolumes = m.DataVolumes
	}
	if w.Networks == nil {
		w.Networks = m.Networks
	}
	if w.SecurityGroups == nil {
		w.SecurityGroups = m.SecurityGroups
	}
	return w
}

// nodeConfig builds the server part shared by masters and workers: plan, disks, networks,
// security groups and project. what is "master" or "worker", for errors.
func (r *clusterResource) nodeConfig(ctx context.Context, b *built, layout client.ClusterLayout, what string, n node, diags *diag.Diagnostics) map[string]any {
	block := r.attrName(what)
	if block == "" {
		block = r.attrName("master") // a Docker cluster's added hosts are its host block's
	}
	at := func(a string) path.Path { return path.Root(block).AtName(a) }
	fail := func(a string, err error) {
		diags.AddAttributeError(at(a), "Invalid "+block+" "+strings.ReplaceAll(a, "_", " "), err.Error())
	}
	config := map[string]any{"resourcePoolId": b.projectID}
	out := map[string]any{"config": config}
	if pl, err := r.client.ClusterPlanByName(ctx, b.place, layout, b.projectID, n.Plan.ValueString()); err != nil {
		fail("plan", err)
	} else {
		out["plan"] = map[string]any{"id": pl.ID}
	}
	// The fixed root disk, then the data disks, as the instance API takes them (CreateCluster.sh:
	// root of a fixed size, storage type 11, datastore "auto").
	specs := []client.VolumeSpec{{Root: true, Name: "root", Size: rootGB, DatastoreID: "auto"}}
	for i, v := range n.DataVolumes {
		spec := client.VolumeSpec{Name: "data-" + strconv.Itoa(i+1), Size: v.Size.ValueInt64(), DatastoreID: "auto"}
		if ds := v.Datastore.ValueString(); ds != "" {
			o, err := r.client.DatastoreByName(ctx, b.place, b.projectID, ds)
			if err != nil {
				fail("data_volume", err)
				continue
			}
			spec.DatastoreID = strconv.FormatInt(o.ID, 10)
		}
		specs = append(specs, spec)
	}
	vols := client.VolumesBody(specs)
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
		// Numeric: the cluster API's worker path refuses "network-40" (CreateCluster.sh).
		nets = append(nets, map[string]any{"primaryInterface": i == 0, "network": map[string]any{"id": nw.ID}})
	}
	out["networkInterfaces"] = nets
	if ids, err := r.client.SecurityGroupExternalIDs(ctx, b.place, b.projectID, n.SecurityGroups); err != nil {
		fail("security_groups", err)
	} else if len(ids) > 0 {
		out["securityGroups"] = ids // the cloud's IDs: the cluster API refuses console IDs
	}
	return out
}

func (r *clusterResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	if !r.ready(&resp.Diagnostics) {
		return
	}
	var m model
	resp.Diagnostics.Append(r.get(ctx, req.Plan, &m)...)
	timeout, d := m.Timeouts.Create(ctx, createTimeout)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}
	parent := ctx
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
	resp.Diagnostics.Append(r.set(ctx, &resp.State, &m)...)
	c, err := r.client.WaitForCluster(ctx, id)
	if err != nil {
		// A warning, not an error: an error would taint the cluster, and the next apply would rebuild it.
		resp.Diagnostics.AddWarning("Cluster not ready yet", fmt.Sprintf("Cluster %d was created but isn't ready: %s. A later refresh reads its status.", id, err))
		if !m.Autoscaling.IsNull() {
			resp.Diagnostics.Append(resp.Private.SetKey(parent, scalerPending, []byte("true"))...)
		}
		// One last read, with a minute of its own, so state holds the hosts made so far and the next
		// plan has nothing to catch up on. Skipped when you stop the run.
		if parent.Err() == nil {
			rctx, rcancel := context.WithTimeout(parent, time.Minute)
			defer rcancel()
			if last, gerr := r.client.GetCluster(rctx, id); gerr == nil {
				var quiet diag.Diagnostics
				r.refresh(rctx, &m, last, false, &quiet)
				resp.Diagnostics.Append(r.set(parent, &resp.State, &m)...)
			}
		}
		return
	}
	if client.ClusterFailed(c.Status) {
		resp.Diagnostics.AddError("Cluster failed", fmt.Sprintf("Cluster %d ended with %q: %s. The next apply replaces it.", id, c.Status, c.StatusMessage))
		return
	}
	// The create sent the range. A range you set is configured again, the way the console's Edit
	// Cluster stores it; held at worker_count, the create's own is enough.
	if !m.Autoscaling.IsNull() {
		if err := r.applyAutoscaling(ctx, id, m, &resp.Diagnostics); err != nil {
			// A warning: an error would taint a cluster that was built fine. Left out of state, the
			// autoscaler is configured by the next apply.
			resp.Diagnostics.AddWarning("Autoscaler not configured",
				fmt.Sprintf("Cluster %d was built, but configuring its autoscaler failed: %s. The next apply tries again.", id, err))
			resp.Diagnostics.Append(resp.Private.SetKey(ctx, scalerPending, []byte("true"))...)
		}
	}
	r.refresh(ctx, &m, c, false, &resp.Diagnostics)
	resp.Diagnostics.Append(r.set(ctx, &resp.State, &m)...)
}

// applyAutoscaling sets a Kubernetes cluster's autoscaler: its range, or held at worker_count when
// autoscaling is left out (as MTN's order builds it). Docker clusters have none.
func (r *clusterResource) applyAutoscaling(ctx context.Context, id int64, m model, diags *diag.Diagnostics) error {
	if r.kind != kube {
		return nil
	}
	lo, hi := scalerRange(ctx, m, diags)
	least, _ := strconv.ParseInt(lo, 10, 64)
	most, _ := strconv.ParseInt(hi, 10, 64)
	return r.client.ConfigureAutoscaler(ctx, id, true, least, most)
}

// scalerRange is the autoscaler's range, as strings: autoscaling's, else worker_count both ways.
func scalerRange(ctx context.Context, m model, diags *diag.Diagnostics) (string, string) {
	n := strconv.FormatInt(m.WorkerCount.ValueInt64(), 10)
	if m.Autoscaling.IsNull() || m.Autoscaling.IsUnknown() {
		return n, n
	}
	var a autoscaling
	diags.Append(m.Autoscaling.As(ctx, &a, basetypes.ObjectAsOptions{})...)
	return strconv.FormatInt(a.MinNodes.ValueInt64(), 10), strconv.FormatInt(a.MaxNodes.ValueInt64(), 10)
}

// withMK8S is labels with MTN's marker label (CreateCluster.sh) in front, or as given when they
// already have it: what is sent is what the plan holds.
func withMK8S(labels []string) []string {
	if slices.ContainsFunc(labels, func(l string) bool { return strings.EqualFold(l, mk8sLabel) }) {
		return labels
	}
	return append([]string{mk8sLabel}, labels...)
}

// mk8sLabel and mk8sDescription are what MTN's own order puts on every managed Kubernetes cluster
// (CreateCluster.sh). The label is added for you and never shown in labels; the description is
// used when you set none.
const (
	mk8sLabel       = "MK8S"
	mk8sDescription = "MK8S Cluster"
)

// refresh copies what MTN Cloud reports. What the nodes were built with isn't readable, so it stays.
// readCount takes worker_count from the workers listed. Only a plain refresh does: after a create
// or an update the plan's count stands, even while workers are still coming.
func (r *clusterResource) refresh(ctx context.Context, m *model, c *client.Cluster, readCount bool, diags *diag.Diagnostics) {
	m.ID, m.Name, m.Status = types.StringValue(strconv.FormatInt(c.ID, 10)), types.StringValue(c.Name), types.StringValue(c.Status)
	m.APIURL = types.StringNull()
	if c.ServiceURL != "" {
		m.APIURL = types.StringValue(c.ServiceURL)
	}
	// MTN's own description, put there for you, isn't shown unless you set one.
	ours := m.Description.IsNull() && c.Description == mk8sDescription && r.kind == kube
	if (c.Description != "" || !m.Description.IsNull()) && !ours {
		m.Description = types.StringValue(c.Description)
	}
	labels := c.Labels
	if r.kind == kube && !slices.ContainsFunc(m.Labels.Elements(), func(v attr.Value) bool {
		s, ok := v.(types.String)
		return ok && strings.EqualFold(s.ValueString(), mk8sLabel)
	}) {
		// MTN's marker label, added for you: shown only if your configuration lists it.
		labels = slices.DeleteFunc(slices.Clone(labels), func(l string) bool { return strings.EqualFold(l, mk8sLabel) })
	}
	if len(labels) > 0 || len(m.Labels.Elements()) > 0 {
		l, d := types.ListValueFrom(ctx, types.StringType, labels)
		diags.Append(d...)
		m.Labels = l
	}
	if r.kind == kube {
		if layouts, err := r.client.ClusterLayouts(ctx); err == nil {
			for _, l := range layouts {
				if l.ID == c.LayoutID {
					key := layoutKey(l)
					if key == "" {
						key = l.Name // a layout other than MTN's three, made in the console
					}
					m.Layout = types.StringValue(key)
				}
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
	resp.Diagnostics.Append(r.get(ctx, req.State, &m)...)
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
	if !c.IsType(typeCodes[r.kind], typeNames[r.kind]) {
		resp.Diagnostics.AddError("Wrong kind of cluster",
			fmt.Sprintf("Cluster %d is a %q, not a %s. Import it with the resource for its kind.", id, c.TypeName, typeNames[r.kind]))
		return
	}
	if m.Timeouts.IsNull() {
		m.Timeouts = timeouts.Value{Object: types.ObjectNull(map[string]attr.Type{
			"create": types.StringType, "update": types.StringType, "delete": types.StringType})}
	}
	r.refresh(ctx, &m, c, true, &resp.Diagnostics)
	resp.Diagnostics.Append(r.set(ctx, &resp.State, &m)...)
}

func (r *clusterResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	if !r.ready(&resp.Diagnostics) {
		return
	}
	var plan, state model
	resp.Diagnostics.Append(r.get(ctx, req.Plan, &plan)...)
	resp.Diagnostics.Append(r.get(ctx, req.State, &state)...)
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
		if r.kind == kube {
			labels = withMK8S(labels)
		}
		desc := plan.Description.ValueString()
		if desc == "" && r.kind == kube {
			desc = mk8sDescription
		}
		if err := r.client.UpdateCluster(ctx, id, plan.Name.ValueString(), desc, labels); err != nil {
			resp.Diagnostics.AddError("Unable to update the cluster", err.Error())
			return
		}
	}
	// Without autoscaling the autoscaler is held at worker_count (MTN's order never turns it off): it
	// moves to the new count first, so it doesn't undo the scaling.
	scalerOff := !state.Autoscaling.IsNull() && plan.Autoscaling.IsNull()
	pinned := plan.Autoscaling.IsNull() && (scalerOff || !plan.WorkerCount.Equal(state.WorkerCount))
	if pinned {
		if err := r.applyAutoscaling(ctx, id, plan, &resp.Diagnostics); err != nil {
			resp.Diagnostics.AddError("Unable to hold the autoscaler at worker_count", err.Error())
			return
		}
	}
	// Held at worker_count, the autoscaler leaves the count to it, wherever it was before.
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
	resp.Diagnostics.Append(r.set(ctx, &resp.State, &plan)...)
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
		if r.kind == docker {
			body = b.body["server"].(map[string]any)
			delete(body, "visibility")
		}
		if b.addType == 0 {
			diags.AddError("Unable to add workers", fmt.Sprintf("Layout %q has no node set that can grow, so MTN Cloud can't add nodes to this cluster.", b.layout))
			return
		}
		// MTN Cloud refuses an add without the node set's server type ("serverType: Server type not
		// specified", QA cluster 148, 2026-11-03); the CLI's add-worker sends the layout's.
		body["serverType"] = map[string]any{"id": b.addType}
		body["nodeCount"] = want - len(workers)
		// The create's config.nodeCount is the total (Docker); adding hosts takes only the outer
		// nodeCount, as the CLI's add-worker sends it (clusters.rb add_worker, POST .../servers).
		delete(body["config"].(map[string]any), "nodeCount")
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
	resp.Diagnostics.Append(r.get(ctx, req.State, &m)...)
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
