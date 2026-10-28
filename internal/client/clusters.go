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
	TypeCode        string // "kubernetes-cluster" or "docker-cluster"
	ProvisionTypeID int64
	Masters         int64 // fixed master nodes; 0 for Docker
	Workers         int64 // default worker (Docker: host) count
	Options         map[string]FormField
	Defaults        map[string]string // option default values by field name
}

type wireClusterLayout struct {
	ID        int64  `json:"id"`
	Name      string `json:"name"`
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
		NodeType  string `json:"nodeType"`
		NodeCount int64  `json:"nodeCount"`
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
			l := ClusterLayout{ID: w.ID, Name: w.Name, TypeCode: w.GroupType.Code, ProvisionTypeID: w.ProvisionType.ID,
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

// ClusterFloatingIPPoolByName resolves a floating IP pool for cluster nodes to its "pool-<id>" value.
func (c *Client) ClusterFloatingIPPoolByName(ctx context.Context, p *Placement, projectID int64, name string) (string, error) {
	opts, err := c.namedValues(ctx, "/api/options/openStack/openstackFloatingIpOptions", // mtncloud:allow-cloud-name (API path)
		url.Values{"zoneId": {itoa(p.CloudID)}, "resourcePoolId": {itoa(projectID)}})
	if err != nil {
		return "", err
	}
	var names []string
	for _, o := range opts {
		if !strings.HasPrefix(o.Value, "pool-") { // headings and single addresses
			continue
		}
		if strings.EqualFold(o.Name, name) {
			return o.Value, nil
		}
		names = append(names, fmt.Sprintf("%q", o.Name))
	}
	return "", &NoMatchError{msg: fmt.Sprintf("no floating IP pool %q: choose one of %s", name, strings.Join(names, ", "))}
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
}

type wireCluster struct {
	ID            int64    `json:"id"`
	Name          string   `json:"name"`
	Description   *string  `json:"description"`
	Labels        []string `json:"labels"`
	Status        string   `json:"status"`
	StatusMessage *string  `json:"statusMessage"`
	ServiceURL    *string  `json:"serviceUrl"`
	Layout        *struct {
		ID int64 `json:"id"`
	} `json:"layout"`
	Type *struct {
		Code string `json:"code"`
	} `json:"type"`
}

func (w wireCluster) cluster() *Cluster {
	c := &Cluster{ID: w.ID, Name: w.Name, Description: deref(w.Description), Labels: w.Labels, Status: w.Status,
		StatusMessage: deref(w.StatusMessage), ServiceURL: deref(w.ServiceURL)}
	if w.Layout != nil {
		c.LayoutID = w.Layout.ID
	}
	if w.Type != nil {
		c.TypeCode = w.Type.Code
	}
	return c
}

// ClusterFailed reports a terminal failure status.
func ClusterFailed(status string) bool {
	return strings.EqualFold(status, "failed") || strings.EqualFold(status, "error")
}

// CreateCluster posts {"cluster": body} and returns the new cluster's ID.
func (c *Client) CreateCluster(ctx context.Context, body map[string]any) (int64, error) {
	var resp struct {
		Cluster struct {
			ID int64 `json:"id"`
		} `json:"cluster"`
	}
	if err := c.do(ctx, http.MethodPost, "/api/clusters", nil, map[string]any{"cluster": body}, &resp); err != nil {
		return 0, err
	}
	return resp.Cluster.ID, nil
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

// WaitForCluster polls a cluster until it is no longer provisioning, retrying a 404 just after the
// create (rule 13). A failed cluster is returned with no error.
func (c *Client) WaitForCluster(ctx context.Context, id int64) (*Cluster, error) {
	return poll(ctx, c, func(ctx context.Context) (*Cluster, error) { return c.GetCluster(ctx, id) },
		func(cl *Cluster, err error) (bool, error) {
			switch {
			case IsNotFound(err):
				return false, nil
			case err != nil:
				return false, err
			}
			s := strings.ToLower(cl.Status)
			return s == "ok" || s == "warning" || ClusterFailed(s), nil
		})
}

// UpdateCluster changes a cluster's name, description and labels.
func (c *Client) UpdateCluster(ctx context.Context, id int64, name, description string, labels []string) error {
	if labels == nil {
		labels = []string{}
	}
	body := map[string]any{"cluster": map[string]any{"name": name, "description": description, "labels": labels}}
	return c.do(ctx, http.MethodPut, "/api/clusters/"+itoa(id), nil, body, nil)
}

// DeleteCluster deletes a cluster with its nodes; one already gone is not an error.
func (c *Client) DeleteCluster(ctx context.Context, id int64) error {
	q := url.Values{"removeResources": {"on"}}
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
	var raw json.RawMessage
	if err := c.get(ctx, "/api/clusters/"+itoa(id)+"/workers", url.Values{"max": {"1000"}}, &raw); err != nil {
		return nil, err
	}
	type wire struct {
		ID     int64  `json:"id"`
		Name   string `json:"name"`
		Status string `json:"status"`
	}
	var list []wire
	if err := json.Unmarshal(raw, &list); err != nil {
		var wrapped struct {
			Workers []wire `json:"workers"`
		}
		if err := json.Unmarshal(raw, &wrapped); err != nil {
			return nil, fmt.Errorf("unexpected workers response: %w", err)
		}
		list = wrapped.Workers
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

// ConfigureAutoscaler turns the cluster's worker autoscaler on (with its node range) or off.
func (c *Client) ConfigureAutoscaler(ctx context.Context, id int64, enabled bool, minNodes, maxNodes int64) error {
	cl := map[string]any{"autoScalerEnabled": enabled}
	if enabled {
		cl["autoScalerGroups"] = []any{map[string]any{"minNodes": minNodes, "maxNodes": maxNodes}}
	}
	return c.do(ctx, http.MethodPost, "/api/clusters/"+itoa(id)+"/configure-autoscaler", nil, map[string]any{"cluster": cl}, nil)
}
