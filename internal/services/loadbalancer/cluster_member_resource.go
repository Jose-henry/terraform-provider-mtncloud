package loadbalancer

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-framework-timeouts/resource/timeouts"
	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64default"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/mtn/terraform-provider-mtncloud/internal/client"
)

var _ resource.ResourceWithImportState = (*clusterMemberResource)(nil)

func newClusterMemberResource() resource.Resource { return &clusterMemberResource{} }

// clusterMemberResource puts one role of a cluster's nodes (for example its Kubernetes workers) in a
// pool, as the console's member picker does: the plugin's clusterGroup "cluster:<id>:<role>" (its
// TERRAFORM-AGENT-GUIDE.md, section 9.6). Like an instance's group, the console keeps it in step as
// the cluster gains or loses nodes, and its members are removed together.
type clusterMemberResource struct{ rbase }

type clusterMemberRes struct {
	ID             types.String   `tfsdk:"id"`
	NetworkID      types.Int64    `tfsdk:"network_id"`
	LoadBalancerID types.String   `tfsdk:"load_balancer_id"`
	PoolID         types.String   `tfsdk:"pool_id"`
	ClusterID      types.Int64    `tfsdk:"cluster_id"`
	Role           types.String   `tfsdk:"role"`
	Port           types.Int64    `tfsdk:"port"`
	Weight         types.Int64    `tfsdk:"weight"`
	Backup         types.Bool     `tfsdk:"backup"`
	Members        types.List     `tfsdk:"members"`
	Timeouts       timeouts.Value `tfsdk:"timeouts"`
}

const clusterMemberKind = "cluster's members"

// nodeRole is a node type as MTN Cloud names it: lower case words joined by "-".
var nodeRole = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

func (r *clusterMemberResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_load_balancer_cluster_member"
}

func (r *clusterMemberResource) Schema(ctx context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "One role of a cluster's nodes in a load balancer pool, for example a Kubernetes cluster's workers: every " +
			"node of that role with an address on the load balancer's network becomes a member, as one group. The console " +
			"keeps the group in step with the cluster: when it gains or loses nodes of that role, they are added to or " +
			"removed from the pool. The cluster must be one of yours (or one shared with every tenant) with nodes on the load " +
			"balancer's network. One per cluster role and pool: the cloud adds and removes a group's members together, so a " +
			"second port for the same role needs `mtncloud_load_balancer_member` per node.",
		Attributes: map[string]schema.Attribute{
			"id":               schema.StringAttribute{Computed: true, PlanModifiers: keepStr, Description: "`<cluster_id>:<role>`."},
			"network_id":       networkAttr(),
			"load_balancer_id": lbIDAttr(),
			"pool_id": schema.StringAttribute{Required: true, PlanModifiers: replaceStr,
				Description: "The pool, for example `mtncloud_load_balancer_pool.web.id`. Changing it replaces the members."},
			"cluster_id": schema.Int64Attribute{Required: true, PlanModifiers: replaceInt, Validators: []validator.Int64{int64validator.AtLeast(1)},
				Description: "The cluster's ID, for example `data.mtncloud_cluster.k8s.id`. Changing it replaces the members."},
			"role": schema.StringAttribute{Required: true, PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
				Validators: []validator.String{stringvalidator.RegexMatches(nodeRole, "must be a node type such as kube-worker")},
				Description: "The nodes' type: `kube-worker` for a Kubernetes cluster's workers, `kube-master` for its masters. " +
					"Changing it replaces the members."},
			"port": schema.Int64Attribute{Required: true, PlanModifiers: fixedInt("member"), Validators: portValid,
				Description: "The port the nodes listen on, for example a Kubernetes NodePort. Set at create: the cloud can't change it, so a change is refused at plan time."},
			// Optional+Computed with a default: most members share traffic equally.
			"weight": schema.Int64Attribute{Optional: true, Computed: true, Default: int64default.StaticInt64(1),
				Validators:  []validator.Int64{int64validator.Between(0, 256)},
				Description: "Each member's share of the traffic, 0 to 256; 0 sends them none. Defaults to `1`. Changed in place on each member."},
			"backup": schema.BoolAttribute{Optional: true, Computed: true, Default: booldefault.StaticBool(false),
				Description: "Only send traffic here when every other member is down. Defaults to `false`. Changed in place on each member."},
			"members": schema.ListNestedAttribute{Computed: true,
				Description: "The role's members now, sorted by address. The list follows the cluster as it scales.",
				NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
					"id":      schema.StringAttribute{Computed: true, Description: "Member ID."},
					"address": schema.StringAttribute{Computed: true, Description: "The node's address."},
				}},
			},
		},
		Blocks: map[string]schema.Block{
			"timeouts": timeouts.Block(ctx, timeouts.Opts{Create: true, Update: true, Delete: true}),
		},
	}
}

// fill reads the group into m. Gone when the pool has none of its members.
func (r *clusterMemberResource) fill(m *clusterMemberRes, lb *client.LoadBalancer) (gone bool) {
	p := findPool(lb, m.PoolID.ValueString())
	if p == nil {
		return true
	}
	ms := client.ClusterMembers(p, m.ClusterID.ValueInt64(), m.Role.ValueString())
	if len(ms) == 0 {
		return true
	}
	m.Members, m.Port, m.Weight, m.Backup = readGroup(ms)
	return false
}

func (r *clusterMemberResource) refresh(ctx context.Context, m *clusterMemberRes, st *tfsdk.State, diags *diag.Diagnostics) {
	if lb, _ := r.readLB(ctx, m.NetworkID.ValueInt64(), m.LoadBalancerID.ValueString(), diags); lb != nil && r.fill(m, lb) {
		missingAfterWrite(diags, clusterMemberKind)
	}
	diags.Append(st.Set(ctx, m)...)
}

func clusterMemberID(m clusterMemberRes) string {
	return strconv.FormatInt(m.ClusterID.ValueInt64(), 10) + ":" + m.Role.ValueString()
}

func (r *clusterMemberResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	if !r.ready(&resp.Diagnostics) {
		return
	}
	var plan clusterMemberRes
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	timeout, d := plan.Timeouts.Create(ctx, lbTimeout)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cid, role := plan.ClusterID.ValueInt64(), plan.Role.ValueString()
	// Members of this group already there: removing either resource would remove both. Adopt them by import.
	if lb, _ := r.readLB(ctx, plan.NetworkID.ValueInt64(), plan.LoadBalancerID.ValueString(), &resp.Diagnostics); lb != nil {
		if p := findPool(lb, plan.PoolID.ValueString()); p != nil && len(client.ClusterMembers(p, cid, role)) > 0 {
			resp.Diagnostics.AddAttributeError(path.Root("role"), "Cluster role already in the pool",
				fmt.Sprintf("Cluster %d's %s nodes already have members in pool %s, and the cloud removes a group's members "+
					"together, so only one resource can own them. To manage them here, import them: terraform import <address> "+
					"%d/%s/%s/%d:%s.", cid, role, plan.PoolID.ValueString(), plan.NetworkID.ValueInt64(), plan.LoadBalancerID.ValueString(),
					plan.PoolID.ValueString(), cid, role))
		}
	}
	if resp.Diagnostics.HasError() {
		return
	}
	err := r.client.AddLBClusterMembers(ctx, plan.NetworkID.ValueInt64(), plan.LoadBalancerID.ValueString(), plan.PoolID.ValueString(),
		client.LBClusterMemberSpec{ClusterID: cid, Role: role, Port: plan.Port.ValueInt64(), Weight: plan.Weight.ValueInt64(), Backup: plan.Backup.ValueBool()})
	if err != nil {
		resp.Diagnostics.AddError("Unable to add the cluster's nodes to the pool", err.Error())
		// The group may have been added before the error: keep it in state when the pool shows it,
		// so the next apply doesn't add it twice. On a context of its own: the create's may be what ran out.
		rctx, rcancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Minute)
		defer rcancel()
		if lb, _ := r.readLB(rctx, plan.NetworkID.ValueInt64(), plan.LoadBalancerID.ValueString(), &diag.Diagnostics{}); lb != nil && !r.fill(&plan, lb) {
			plan.ID = types.StringValue(clusterMemberID(plan))
			resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
		}
		return
	}
	plan.ID = types.StringValue(clusterMemberID(plan))
	r.refresh(ctx, &plan, &resp.State, &resp.Diagnostics)
}

func (r *clusterMemberResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	if !r.ready(&resp.Diagnostics) {
		return
	}
	var state clusterMemberRes
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	lb, gone := r.readLB(ctx, state.NetworkID.ValueInt64(), state.LoadBalancerID.ValueString(), &resp.Diagnostics)
	if gone || (lb != nil && r.fill(&state, lb)) {
		resp.State.RemoveResource(ctx)
		return
	}
	if lb != nil {
		resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
	}
}

// Update changes weight and backup on each member in place; the rest is fixed or a parent.
func (r *clusterMemberResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state clusterMemberRes
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	plan.ID, plan.Members = state.ID, state.Members
	if plan.Weight.Equal(state.Weight) && plan.Backup.Equal(state.Backup) {
		resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
		return
	}
	if !r.ready(&resp.Diagnostics) {
		return
	}
	timeout, d := plan.Timeouts.Update(ctx, lbTimeout)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if err := r.setGroupWeight(ctx, plan.NetworkID.ValueInt64(), plan.LoadBalancerID.ValueString(), plan.PoolID.ValueString(),
		state.Members, plan.Weight.ValueInt64(), plan.Backup.ValueBool()); err != nil {
		resp.Diagnostics.AddError("Unable to change the cluster's members", err.Error()+".")
		rctx, rcancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Minute)
		defer rcancel()
		r.refresh(rctx, &state, &resp.State, &resp.Diagnostics)
		return
	}
	r.refresh(ctx, &plan, &resp.State, &resp.Diagnostics)
}

func (r *clusterMemberResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	if !r.ready(&resp.Diagnostics) {
		return
	}
	var state clusterMemberRes
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	timeout, d := state.Timeouts.Delete(ctx, lbTimeout)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if err := r.client.RemoveLBClusterMembers(ctx, state.NetworkID.ValueInt64(), state.LoadBalancerID.ValueString(), state.PoolID.ValueString(),
		state.ClusterID.ValueInt64(), state.Role.ValueString()); err != nil {
		resp.Diagnostics.AddError("Unable to remove the cluster's nodes from the pool", err.Error())
	}
}

// ImportState takes <network_id>/<load_balancer_id>/<pool_id>/<cluster_id>:<role>.
func (r *clusterMemberResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	importIDs(req, resp, "<network_id>/<load_balancer_id>/<pool_id>/<cluster_id>:<role>", "network_id", "load_balancer_id", "pool_id", "id")
	if resp.Diagnostics.HasError() {
		return
	}
	var id types.String
	resp.Diagnostics.Append(resp.State.GetAttribute(ctx, path.Root("id"), &id)...)
	c, role, ok := strings.Cut(id.ValueString(), ":")
	cid, err := strconv.ParseInt(c, 10, 64)
	if !ok || err != nil || cid < 1 || !nodeRole.MatchString(role) {
		resp.Diagnostics.AddError("Invalid import ID", "The last part must be <cluster_id>:<role>, for example 143:kube-worker, got "+
			strconv.Quote(id.ValueString())+".")
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("cluster_id"), cid)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("role"), role)...)
}
