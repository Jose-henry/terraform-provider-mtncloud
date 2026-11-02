package cluster

import (
	"context"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-timeouts/resource/timeouts"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/mtn/terraform-provider-mtncloud/internal/client"
)

// The two kinds of cluster MTN Cloud builds (owner, 2026-11-02): mtncloud_managed_kubernetes_cluster
// and mtncloud_docker_cluster. They share one implementation; this file holds what differs.
const (
	kube   = "kubernetes"
	docker = "docker"
)

// typeCodes maps a kind to the platform's cluster type code.
var typeCodes = map[string]string{kube: "kubernetes-cluster", docker: "docker-cluster"}

// typeNames are the platform's names of those types, which the cluster list shows instead of codes.
var typeNames = map[string]string{kube: "Kubernetes Cluster", docker: "Docker Cluster"}

// mtnLayout is one of MTN's cluster layouts: found by its code, else by its name (B-033).
type mtnLayout struct{ code, name string }

// kubeLayouts are the Kubernetes layouts by the value `layout` takes (live, 2026-11-02; the codes
// match `Manage Kubernetes/CreateCluster.sh`: single 315, ha3 313, ha5 314).
var kubeLayouts = map[string]mtnLayout{
	"single": {"8b84f285-45f9-408a-8266-7d07b1431794", "MTN Kubernetes Single-Master"},
	"ha3":    {"0f0e59cc-3f26-4017-9584-acc8b197c24e", "MTN Kubernetes HA (3)"},
	"ha5":    {"6df9e665-859a-4e6e-a353-b232d18d88c6", "MTN Kubernetes HA (5)"},
}

// dockerLayout is MTN's one Docker layout: hosts only, no masters (layout 311).
var dockerLayout = mtnLayout{"254b4278-d04f-4341-b53b-c3e1a04c652a", "MTN Docker Cluster"}

// layoutKey is the `layout` value of a layout, matched on its code, else its name (a layout MTN
// re-created keeps its name), or "" for a layout that isn't one of MTN's three.
func layoutKey(l client.ClusterLayout) string {
	for k, m := range kubeLayouts {
		if l.Code != "" && l.Code == m.code {
			return k
		}
	}
	for k, m := range kubeLayouts {
		if strings.EqualFold(l.Name, m.name) {
			return k
		}
	}
	return ""
}

// attrName is the attribute a model field is shown as: a Docker cluster has hosts, not masters and
// workers. "" when the kind has no such attribute.
func (r *clusterResource) attrName(a string) string {
	if r.kind == kube {
		return a
	}
	switch a {
	case "master":
		return "host"
	case "worker_count":
		return "host_count"
	case "worker_ids":
		return "host_ids"
	case "worker", "layout", "kubernetes_version", "pod_cidr", "service_cidr", "registry", "autoscaling", "api_url":
		return ""
	}
	return a
}

// at is the path of a model field's attribute; an error on a field the kind doesn't have goes on
// the resource.
func (r *clusterResource) addError(diags *diag.Diagnostics, a, summary, detail string) {
	if n := r.attrName(a); n != "" {
		diags.AddAttributeError(path.Root(n), summary, detail)
		return
	}
	diags.AddError(summary, detail)
}

// dockerModel is a Docker cluster: its hosts are the model's master settings and worker count.
type dockerModel struct {
	ID           types.String   `tfsdk:"id"`
	Name         types.String   `tfsdk:"name"`
	Group        types.String   `tfsdk:"group"`
	Cloud        types.String   `tfsdk:"cloud"`
	Project      types.String   `tfsdk:"project"`
	Description  types.String   `tfsdk:"description"`
	Labels       types.List     `tfsdk:"labels"`
	ResourceName types.String   `tfsdk:"resource_name"`
	Hostname     types.String   `tfsdk:"hostname"`
	Domain       types.String   `tfsdk:"domain"`
	Workflow     types.String   `tfsdk:"workflow"`
	CreateUser   types.Bool     `tfsdk:"create_user"`
	Host         types.Object   `tfsdk:"host"`
	HostCount    types.Int64    `tfsdk:"host_count"`
	Status       types.String   `tfsdk:"status"`
	HostIDs      types.List     `tfsdk:"host_ids"`
	Timeouts     timeouts.Value `tfsdk:"timeouts"`
}

func (d dockerModel) model() model {
	return model{ID: d.ID, Name: d.Name, Group: d.Group, Cloud: d.Cloud, Project: d.Project, Description: d.Description,
		Labels: d.Labels, ResourceName: d.ResourceName, Hostname: d.Hostname, Domain: d.Domain, Workflow: d.Workflow,
		CreateUser: d.CreateUser, Master: d.Host, WorkerCount: d.HostCount, Status: d.Status,
		WorkerIDs: d.HostIDs, Timeouts: d.Timeouts,
		Layout: types.StringNull(), KubernetesVersion: types.StringNull(), PodCIDR: types.StringNull(),
		ServiceCIDR: types.StringNull(), Registry: types.StringNull(), APIURL: types.StringNull(),
		Worker: types.ObjectNull(nodeTypes()), Autoscaling: types.ObjectNull(autoscalingTypes)}
}

func dockerOf(m model) dockerModel {
	return dockerModel{ID: m.ID, Name: m.Name, Group: m.Group, Cloud: m.Cloud, Project: m.Project, Description: m.Description,
		Labels: m.Labels, ResourceName: m.ResourceName, Hostname: m.Hostname, Domain: m.Domain, Workflow: m.Workflow,
		CreateUser: m.CreateUser, Host: m.Master, HostCount: m.WorkerCount, Status: m.Status,
		HostIDs: m.WorkerIDs, Timeouts: m.Timeouts}
}

var autoscalingTypes = map[string]attr.Type{"min_nodes": types.Int64Type, "max_nodes": types.Int64Type}

// nodeTypes are the attribute types of a master, worker or host block.
func nodeTypes() map[string]attr.Type {
	out := map[string]attr.Type{}
	for k, a := range nodeAttrs(true) {
		out[k] = a.GetType()
	}
	return out
}

type getter interface {
	Get(context.Context, any) diag.Diagnostics
}

type setter interface {
	Set(context.Context, any) diag.Diagnostics
}

// get reads a plan, state or configuration into the shared model.
func (r *clusterResource) get(ctx context.Context, src getter, m *model) diag.Diagnostics {
	if r.kind == kube {
		return src.Get(ctx, m)
	}
	var d dockerModel
	diags := src.Get(ctx, &d)
	*m = d.model()
	return diags
}

// set writes the shared model to a plan or state.
func (r *clusterResource) set(ctx context.Context, dst setter, m *model) diag.Diagnostics {
	if r.kind == kube {
		return dst.Set(ctx, m)
	}
	d := dockerOf(*m)
	return dst.Set(ctx, &d)
}
