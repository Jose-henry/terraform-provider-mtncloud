package client

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
)

// ClusterLayout is one of MTN's cluster layouts: "MTN Kubernetes Single-Master", "MTN Kubernetes HA
// (3)", "MTN Kubernetes HA (5)" and "MTN Docker Cluster" (captured 2026-10-28).
type ClusterLayout struct {
	ID              int64
	Name            string
	Code            string // stable across renames, for example "8b84f285-..." (MTN Kubernetes Single-Master)
	TypeCode        string // "kubernetes-cluster" or "docker-cluster"
	ProvisionTypeID int64
	Masters         int64 // fixed master nodes; 0 for Docker
	Workers         int64 // default worker (Docker: host) count
	// AddServerType is the server type of the layout's first node set that can grow (its workers,
	// a Docker cluster's hosts): adding nodes must name it, as the CLI's add-worker does. 0 if none.
	AddServerType int64
	Options       map[string]FormField
	Defaults      map[string]string // option default values by field name
}

type wireClusterLayout struct {
	ID        int64  `json:"id"`
	Name      string `json:"name"`
	Code      string `json:"code"`
	GroupType *struct {
		Code string `json:"code"`
	} `json:"groupType"`
	ProvisionType *struct {
		ID   int64  `json:"id"`
		Code string `json:"code"`
	} `json:"provisionType"`
	OptionTypes []struct {
		ID           int64  `json:"id"`
		FieldName    string `json:"fieldName"`
		Type         string `json:"type"`
		DefaultValue any    `json:"defaultValue"`
	} `json:"optionTypes"`
	ComputeServers []struct {
		NodeType          string `json:"nodeType"`
		NodeCount         int64  `json:"nodeCount"`
		DynamicCount      bool   `json:"dynamicCount"`
		ComputeServerType *struct {
			ID int64 `json:"id"`
		} `json:"computeServerType"`
	} `json:"computeServers"`
}

// ClusterLayouts lists MTN's own cluster layouts on this cloud: the platform also lists layouts
// for other clouds, which MTN Cloud can't provision. Cached per provider instance.
func (c *Client) ClusterLayouts(ctx context.Context) ([]ClusterLayout, error) {
	return cached(ctx, &c.cache, "cluster-layouts", func(ctx context.Context) ([]ClusterLayout, error) {
		items, err := listAll[wireClusterLayout](ctx, c, "/api/library/cluster-layouts", "layouts", nil)
		if err != nil {
			return nil, err
		}
		var out []ClusterLayout
		for _, w := range items {
			if w.GroupType == nil || w.ProvisionType == nil || !strings.HasPrefix(w.Name, "MTN ") ||
				w.ProvisionType.Code != "openstack" || // mtncloud:allow-cloud-name (provision type wire code)
				(w.GroupType.Code != "kubernetes-cluster" && w.GroupType.Code != "docker-cluster") {
				continue
			}
			l := ClusterLayout{ID: w.ID, Name: w.Name, Code: w.Code, TypeCode: w.GroupType.Code, ProvisionTypeID: w.ProvisionType.ID,
				Options: map[string]FormField{}, Defaults: map[string]string{}}
			for _, o := range w.OptionTypes {
				l.Options[o.FieldName] = FormField{ID: o.ID, Name: o.FieldName, Type: o.Type}
				if o.DefaultValue != nil {
					l.Defaults[o.FieldName] = fmt.Sprint(o.DefaultValue)
				}
			}
			for _, s := range w.ComputeServers {
				if s.NodeType == "master" {
					l.Masters += s.NodeCount
				} else {
					l.Workers += s.NodeCount
				}
				if s.DynamicCount && s.ComputeServerType != nil && l.AddServerType == 0 {
					l.AddServerType = s.ComputeServerType.ID
				}
			}
			out = append(out, l)
		}
		slices.SortFunc(out, func(a, b ClusterLayout) int { return strings.Compare(a.Name, b.Name) })
		return out, nil
	})
}

// ClusterLayoutByName finds one of MTN's layouts of a cluster type ("kubernetes-cluster" or
// "docker-cluster"), case-insensitively.
func (c *Client) ClusterLayoutByName(ctx context.Context, typeCode, name string) (ClusterLayout, error) {
	all, err := c.ClusterLayouts(ctx)
	if err != nil {
		return ClusterLayout{}, err
	}
	var names []string
	for _, l := range all {
		if l.TypeCode != typeCode {
			continue
		}
		if strings.EqualFold(l.Name, name) {
			return l, nil
		}
		names = append(names, fmt.Sprintf("%q", l.Name))
	}
	return ClusterLayout{}, &NoMatchError{msg: fmt.Sprintf("no cluster layout %q: choose one of %s", name, strings.Join(names, ", "))}
}

// ClusterLayoutByCode finds one of MTN's layouts of a cluster type by its code, else by its name
// (B-033: a code survives a rename, a name survives a re-created layout).
func (c *Client) ClusterLayoutByCode(ctx context.Context, typeCode, code, name string) (ClusterLayout, error) {
	all, err := c.ClusterLayouts(ctx)
	if err != nil {
		return ClusterLayout{}, err
	}
	for _, l := range all {
		if code != "" && l.TypeCode == typeCode && strings.EqualFold(l.Code, code) {
			return l, nil
		}
	}
	return c.ClusterLayoutByName(ctx, typeCode, name)
}

// ClusterPlanByName resolves a plan for a cluster layout in a project. The list is only filtered
// when the project is given: without it the platform answers every plan of every price set.
func (c *Client) ClusterPlanByName(ctx context.Context, p *Placement, l ClusterLayout, projectID int64, name string) (Option, error) {
	q := url.Values{"zoneId": {itoa(p.CloudID)}, "siteId": {itoa(p.GroupID)}, "provisionTypeId": {itoa(l.ProvisionTypeID)},
		"layoutId": {itoa(l.ID)}, "resourcePoolId": {itoa(projectID)}}
	opts, err := c.namedValues(ctx, "/api/options/servicePlans", q)
	if err != nil {
		return Option{}, err
	}
	var names []string
	for _, o := range opts {
		if strings.EqualFold(o.Name, name) {
			id, err := strconv.ParseInt(o.Value, 10, 64)
			if err != nil {
				return Option{}, fmt.Errorf("plan %q has a non-numeric ID %q", o.Name, o.Value)
			}
			return Option{ID: id, Name: o.Name}, nil
		}
		if !slices.Contains(names, o.Name) {
			names = append(names, o.Name)
		}
	}
	return Option{}, &NoMatchError{msg: fmt.Sprintf("no plan %q for clusters in this project: choose one of %s", name, strings.Join(names, ", "))}
}

// Registry is a container registry a Kubernetes cluster can pull from. The public Docker Hub has
// no ID: it is the default.
type Registry struct {
	ID   int64 // 0 for the public default
	Name string
}

// Registries lists the registries a cluster can use (the console's "Cluster Repo Account").
func (c *Client) Registries(ctx context.Context, p *Placement) ([]Registry, error) {
	opts, err := c.namedValues(ctx, "/api/options/dockerHubRegistries", url.Values{"zoneId": {itoa(p.CloudID)}, "siteId": {itoa(p.GroupID)}})
	if err != nil {
		return nil, err
	}
	out := make([]Registry, 0, len(opts))
	for _, o := range opts {
		r := Registry{Name: o.Name}
		if o.Value != "" {
			if r.ID, err = strconv.ParseInt(o.Value, 10, 64); err != nil {
				return nil, fmt.Errorf("registry %q has a non-numeric ID %q", o.Name, o.Value)
			}
		}
		out = append(out, r)
	}
	return out, nil
}

// RegistryByName finds a registry, case-insensitively.
func (c *Client) RegistryByName(ctx context.Context, p *Placement, name string) (Registry, error) {
	all, err := c.Registries(ctx, p)
	if err != nil {
		return Registry{}, err
	}
	var names []string
	for _, r := range all {
		if strings.EqualFold(r.Name, name) {
			return r, nil
		}
		names = append(names, fmt.Sprintf("%q", r.Name))
	}
	return Registry{}, &NoMatchError{msg: fmt.Sprintf("no registry %q: choose one of %s", name, strings.Join(names, ", "))}
}

// WorkflowByName finds a provisioning workflow that can run after a cluster's nodes are built.
func (c *Client) WorkflowByName(ctx context.Context, name string) (Option, error) {
	opts, err := c.namedValues(ctx, "/api/options/taskSets", url.Values{"phase": {"postProvision"}})
	if err != nil {
		return Option{}, err
	}
	var names []string
	for _, o := range opts {
		if strings.EqualFold(o.Name, name) {
			id, err := strconv.ParseInt(o.Value, 10, 64)
			if err != nil {
				return Option{}, fmt.Errorf("workflow %q has a non-numeric ID %q", o.Name, o.Value)
			}
			return Option{ID: id, Name: o.Name}, nil
		}
		names = append(names, fmt.Sprintf("%q", o.Name))
	}
	return Option{}, &NoMatchError{msg: fmt.Sprintf("no workflow %q: choose one of %s", name, strings.Join(names, ", "))}
}

// namedValues reads an options endpoint as name/value pairs, the values as text.
func (c *Client) namedValues(ctx context.Context, path string, q url.Values) ([]NamedValue, error) {
	var resp struct {
		Data []struct {
			Name  string `json:"name"`
			Value any    `json:"value"`
		} `json:"data"`
	}
	if err := c.get(ctx, path, q, &resp); err != nil {
		return nil, err
	}
	out := make([]NamedValue, 0, len(resp.Data))
	for _, d := range resp.Data {
		v := ""
		switch x := d.Value.(type) {
		case nil:
		case float64:
			v = strconv.FormatInt(int64(x), 10)
		default:
			v = fmt.Sprint(x)
		}
		out = append(out, NamedValue{Name: d.Name, Value: v})
	}
	return out, nil
}

// Cluster is what GET /api/clusters/{id} reports that the provider uses.
type Cluster struct {
	ID            int64
	Name          string
	Description   string
	Labels        []string
	Status        string
	StatusMessage string
	ServiceURL    string
	LayoutID      int64
	TypeCode      string
	TypeName      string // the list carries only the name, a single read the code too
	// ProvisionComplete is the platform's own word that every node was built.
	ProvisionComplete bool
}

// kubeSyncDefect is the message of MTN Cloud's Kubernetes inventory-sync defect: the cluster is
// built, every node provisioned, and the cluster still says "error" (clusters 143, 144, 168, 169, 171;
// MTN's cluster API reference, finding B). It is the console's view, not the cluster's.
const kubeSyncDefect = "no kube masters found"

// SyncDefect reports a Kubernetes cluster that finished building but shows MTN Cloud's sync defect
// ("No kube masters found"). Whether its nodes are all provisioned is checked separately (Healthy).
func (c *Cluster) SyncDefect() bool {
	return c != nil && strings.EqualFold(c.Status, "error") && c.ProvisionComplete &&
		strings.Contains(strings.ToLower(c.StatusMessage), kubeSyncDefect)
}

// Usable reports a cluster that can be used and changed: "ok", or built with only the sync defect
// (owner, 2026-11-11: that error must not fail a build whose every step succeeded).
func (c *Cluster) Usable() bool {
	return c != nil && (strings.EqualFold(c.Status, "ok") || c.SyncDefect())
}

// IsType reports whether the cluster is of the type with this code or name; a cluster that says
// neither is taken to be of it.
func (c *Cluster) IsType(code, name string) bool {
	if c.TypeCode == "" && c.TypeName == "" {
		return true
	}
	return strings.EqualFold(c.TypeCode, code) || (c.TypeCode == "" && strings.EqualFold(c.TypeName, name))
}

type wireCluster struct {
	ID            int64    `json:"id"`
	Name          string   `json:"name"`
	Description   *string  `json:"description"`
	Labels        []string `json:"labels"`
	Status        string   `json:"status"`
	StatusMessage *string  `json:"statusMessage"`
	ServiceURL    *string  `json:"serviceUrl"`
	Provisioned   bool     `json:"provisionComplete"`
	Layout        *struct {
		ID int64 `json:"id"`
	} `json:"layout"`
	Type *struct {
		Code string `json:"code"`
		Name string `json:"name"`
	} `json:"type"`
}

func (w wireCluster) cluster() *Cluster {
	c := &Cluster{ID: w.ID, Name: w.Name, Description: deref(w.Description), Labels: w.Labels, Status: w.Status,
		StatusMessage: deref(w.StatusMessage), ServiceURL: deref(w.ServiceURL), ProvisionComplete: w.Provisioned}
	if w.Layout != nil {
		c.LayoutID = w.Layout.ID
	}
	if w.Type != nil {
		c.TypeCode, c.TypeName = w.Type.Code, w.Type.Name
	}
	return c
}

// ClusterFailed reports a terminal failure status.
func ClusterFailed(status string) bool {
	return strings.EqualFold(status, "failed") || strings.EqualFold(status, "error")
}

// CreateCluster posts {"cluster": body} and returns the new cluster's ID.
//
// Two creates adding the same new label at once make the platform refuse the second (labelRace). It
// is sent again, a few times, only once a lookup by name shows no cluster was made.
func (c *Client) CreateCluster(ctx context.Context, body map[string]any) (int64, error) {
	name, _ := body["name"].(string)
	for attempt := 1; ; attempt++ {
		var resp struct {
			Cluster struct {
				ID int64 `json:"id"`
			} `json:"cluster"`
		}
		err := c.do(ctx, http.MethodPost, "/api/clusters", nil, map[string]any{"cluster": body}, &resp)
		if err == nil {
			return resp.Cluster.ID, nil
		}
		if !labelRace(err) || attempt >= 3 || name == "" {
			return 0, err
		}
		if same, ferr := c.FindClustersNamed(ctx, name); ferr != nil || len(same) > 0 {
			return 0, err
		}
		if serr := sleep(ctx, backoff(attempt, c.cfg.BackoffBase, c.cfg.BackoffMax, "")); serr != nil {
			return 0, serr
		}
	}
}

// GetCluster reads one cluster.
func (c *Client) GetCluster(ctx context.Context, id int64) (*Cluster, error) {
	var resp struct {
		Cluster wireCluster `json:"cluster"`
	}
	if err := c.get(ctx, "/api/clusters/"+itoa(id), nil, &resp); err != nil {
		return nil, err
	}
	return resp.Cluster.cluster(), nil
}

// FindClustersNamed lists the clusters with this exact name.
func (c *Client) FindClustersNamed(ctx context.Context, name string) ([]*Cluster, error) {
	items, err := listAll[wireCluster](ctx, c, "/api/clusters", "clusters", url.Values{"name": {name}})
	if err != nil {
		return nil, err
	}
	var out []*Cluster
	for _, w := range items {
		if w.Name == name {
			out = append(out, w.cluster())
		}
	}
	return out, nil
}

// UpdateCluster changes a cluster's name, description and labels.
func (c *Client) UpdateCluster(ctx context.Context, id int64, name, description string, labels []string) error {
	if labels == nil {
		labels = []string{}
	}
	body := map[string]any{"cluster": map[string]any{"name": name, "description": description, "labels": labels}}
	return c.do(ctx, http.MethodPut, "/api/clusters/"+itoa(id), nil, body, nil)
}

// DeleteCluster force-deletes a cluster with its infrastructure and associated instances, volumes
// included (owner, 2026-11-03): the CLI's `clusters remove --remove-resources on --remove-instances on -f`
// (dry run, 9.0.0). Force lets a cluster stuck building be deleted. One already gone is not an error.
func (c *Client) DeleteCluster(ctx context.Context, id int64) error {
	q := url.Values{"removeResources": {"on"}, "removeInstances": {"on"}, "force": {"on"}}
	return ignoreNotFound(c.do(ctx, http.MethodDelete, "/api/clusters/"+itoa(id), q, nil, nil))
}

// WaitForClusterGone polls until a deleted cluster is gone.
func (c *Client) WaitForClusterGone(ctx context.Context, id int64) error {
	_, err := poll(ctx, c, func(ctx context.Context) (*Cluster, error) { return c.GetCluster(ctx, id) },
		func(_ *Cluster, err error) (bool, error) {
			if IsNotFound(err) {
				return true, nil
			}
			return false, err
		})
	return err
}

// ClusterNode is a cluster's worker (or Docker host).
type ClusterNode struct {
	ID     int64
	Name   string
	Status string
}

// ClusterWorkers lists a cluster's workers. The reference shows a bare array; the CLI reads
// {"workers": [...]}. Both are accepted.
func (c *Client) ClusterWorkers(ctx context.Context, id int64) ([]ClusterNode, error) {
	return c.clusterNodes(ctx, id, "workers")
}

// ClusterMasters lists a Kubernetes cluster's masters, as ClusterWorkers lists its workers.
func (c *Client) ClusterMasters(ctx context.Context, id int64) ([]ClusterNode, error) {
	return c.clusterNodes(ctx, id, "masters")
}

func (c *Client) clusterNodes(ctx context.Context, id int64, kind string) ([]ClusterNode, error) {
	var raw json.RawMessage
	if err := c.get(ctx, "/api/clusters/"+itoa(id)+"/"+kind, url.Values{"max": {"1000"}}, &raw); err != nil {
		return nil, err
	}
	type wire struct {
		ID     int64  `json:"id"`
		Name   string `json:"name"`
		Status string `json:"status"`
	}
	var list []wire
	if err := json.Unmarshal(raw, &list); err != nil {
		// {"workers": [...], "meta": {...}}: only the list is read (QA t160: meta is an object).
		var wrapped map[string]json.RawMessage
		if err := json.Unmarshal(raw, &wrapped); err != nil {
			return nil, fmt.Errorf("unexpected %s response: %w", kind, err)
		}
		inner, ok := wrapped[kind]
		if !ok || string(inner) == "null" {
			// Never read as "no workers": that would plan new ones.
			return nil, fmt.Errorf("unexpected %s response: no %s list", kind, kind)
		}
		if err := json.Unmarshal(inner, &list); err != nil {
			return nil, fmt.Errorf("unexpected %s response: %w", kind, err)
		}
	}
	out := make([]ClusterNode, 0, len(list))
	for _, w := range list {
		out = append(out, ClusterNode(w))
	}
	return out, nil
}

// AddClusterWorkers adds workers: POST /api/clusters/{id}/servers with {"server": body}.
func (c *Client) AddClusterWorkers(ctx context.Context, id int64, body map[string]any) error {
	return c.do(ctx, http.MethodPost, "/api/clusters/"+itoa(id)+"/servers", nil, map[string]any{"server": body}, nil)
}

// DeleteClusterWorker removes one worker and its VM.
func (c *Client) DeleteClusterWorker(ctx context.Context, id, serverID int64) error {
	q := url.Values{"removeResources": {"on"}}
	return ignoreNotFound(c.do(ctx, http.MethodDelete, "/api/clusters/"+itoa(id)+"/servers/"+itoa(serverID), q, nil, nil))
}

// WaitForWorkersGone waits until none of these workers is listed any more (none: returns at once).
func (c *Client) WaitForWorkersGone(ctx context.Context, id int64, ids []int64) error {
	if len(ids) == 0 {
		return nil
	}
	_, err := poll(ctx, c, func(ctx context.Context) ([]ClusterNode, error) { return c.ClusterWorkers(ctx, id) },
		func(ws []ClusterNode, err error) (bool, error) {
			if err != nil {
				return false, err
			}
			return !slices.ContainsFunc(ws, func(w ClusterNode) bool { return slices.Contains(ids, w.ID) }), nil
		})
	return err
}

// ConfigureAutoscaler turns the cluster's worker autoscaler on (with its node range) or off.
func (c *Client) ConfigureAutoscaler(ctx context.Context, id int64, enabled bool, minNodes, maxNodes int64) error {
	cl := map[string]any{"autoScalerEnabled": enabled}
	if enabled {
		cl["autoScalerGroups"] = []any{map[string]any{"minNodes": minNodes, "maxNodes": maxNodes}}
	}
	return c.do(ctx, http.MethodPost, "/api/clusters/"+itoa(id)+"/configure-autoscaler", nil, map[string]any{"cluster": cl}, nil)
}

// ClusterHealth is a cluster and its workers (Docker: its hosts), read together.
type ClusterHealth struct {
	Cluster *Cluster
	Workers []ClusterNode
	Masters []ClusterNode // read only for a cluster with the sync defect
}

// Failed reports the cluster, or one of its workers, ending in a failed state.
func (h *ClusterHealth) Failed() bool {
	if h == nil || h.Cluster == nil {
		return false
	}
	if ClusterFailed(h.Cluster.Status) && !h.Cluster.SyncDefect() {
		return true
	}
	failed := func(w ClusterNode) bool { return ClusterFailed(w.Status) }
	return slices.ContainsFunc(h.Workers, failed) || slices.ContainsFunc(h.Masters, failed)
}

// FailedWorkers names the workers (and masters) that failed.
func (h *ClusterHealth) FailedWorkers() []string {
	var out []string
	for _, w := range slices.Concat(h.Masters, h.Workers) {
		if ClusterFailed(w.Status) {
			out = append(out, w.Name)
		}
	}
	return out
}

// Healthy reports a cluster that finished building and works: the cluster usable and want workers, every
// one "provisioned" (want < 0: any number). A cluster's own status isn't enough: a cluster stays "ok"
// while a worker is added. One with only the sync defect needs its masters read and provisioned too.
func (h *ClusterHealth) Healthy(want int) bool {
	if h == nil || !h.Cluster.Usable() || (want >= 0 && len(h.Workers) != want) {
		return false
	}
	if h.Cluster.SyncDefect() && len(h.Masters) == 0 {
		return false
	}
	pending := func(w ClusterNode) bool { return !strings.EqualFold(w.Status, "provisioned") }
	return !slices.ContainsFunc(h.Workers, pending) && !slices.ContainsFunc(h.Masters, pending)
}

// WaitForClusterHealthy polls the cluster and its workers until it is healthy with want workers, or it
// or a worker fails. A 404 is read again only just after the create (created, rule 13); otherwise the
// cluster is gone and the wait ends.
func (c *Client) WaitForClusterHealthy(ctx context.Context, id int64, want int, created bool) (*ClusterHealth, error) {
	return poll(ctx, c, func(ctx context.Context) (*ClusterHealth, error) {
		cl, err := c.GetCluster(ctx, id)
		if err != nil {
			return nil, err
		}
		ws, err := c.ClusterWorkers(ctx, id)
		if err != nil {
			return nil, err
		}
		h := &ClusterHealth{Cluster: cl, Workers: ws}
		if cl.SyncDefect() {
			if h.Masters, err = c.ClusterMasters(ctx, id); err != nil {
				return nil, err
			}
		}
		return h, nil
	}, func(h *ClusterHealth, err error) (bool, error) {
		switch {
		case IsNotFound(err) && created:
			return false, nil
		case err != nil:
			return false, err
		}
		return h.Failed() || h.Healthy(want), nil
	})
}
