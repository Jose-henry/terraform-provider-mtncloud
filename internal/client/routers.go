package client

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
)

// MTN Routers: POST/GET/PUT/DELETE /api/networks/routers[/{id}]. Shapes were captured as a customer
// (sub-tenant) on MTN Cloud, 2026-10-27, including two owner-approved live runs: see
// dev-docs/api-research.md. A router starts "provisioning" and reaches "ok" within seconds.

// Router is an MTN Router (a Neutron router) as the API returns it.
type Router struct {
	ID          int64
	Name        string
	Description string
	Status      string
	Enabled     bool
	ExternalID  string // Neutron UUID, empty while provisioning
	// The gateway. ExternalNetworkID is 0 without one. ExternalIP comes from the gateway interface:
	// the API's own externalIp keeps the old address after the gateway is removed.
	ExternalNetworkID   int64
	ExternalNetworkName string
	ExternalIP          string
	NetworkIDs          []int64 // internal networks, in the API's order
}

// RouterInput is a router to create, or the new settings of one. The project and the name can't
// change after create.
type RouterInput struct {
	Name              string // create only
	Description       string
	ProjectID         int64 // create only
	Enabled           bool
	ExternalNetworkID int64   // 0 for no gateway
	SubnetIDs         []int64 // the subnets of the internal networks
	// KeepGateway leaves the gateway out of an update, so an unchanged one is never set again
	// (which could hand the router a new external IP). A PUT without it keeps the gateway.
	KeepGateway bool
}

type wireRouter struct {
	ID              int64    `json:"id"`
	Name            string   `json:"name"`
	Description     *string  `json:"description"`
	Status          string   `json:"status"`
	Enabled         bool     `json:"enabled"`
	ExternalID      *string  `json:"externalId"`
	ExternalNetwork *wireRef `json:"externalNetwork"`
	Interfaces      []struct {
		IPAddress *string  `json:"ipAddress"`
		Network   *wireRef `json:"network"`
	} `json:"interfaces"`
}

func (w wireRouter) router() *Router {
	r := &Router{
		ID: w.ID, Name: w.Name, Description: deref(w.Description), Status: w.Status, Enabled: w.Enabled,
		ExternalID: deref(w.ExternalID),
	}
	if w.ExternalNetwork != nil {
		r.ExternalNetworkID, r.ExternalNetworkName = w.ExternalNetwork.ID, w.ExternalNetwork.Name
	}
	for _, i := range w.Interfaces {
		switch {
		case i.Network == nil:
		case r.ExternalNetworkID != 0 && i.Network.ID == r.ExternalNetworkID:
			r.ExternalIP = deref(i.IPAddress) // the gateway interface
		default:
			r.NetworkIDs = append(r.NetworkIDs, i.Network.ID)
		}
	}
	return r
}

// RouterFailed reports a router the platform marks as broken. Healthy routers report "ok".
func RouterFailed(status string) bool {
	switch strings.ToLower(status) {
	case "failed", "error":
		return true
	}
	return false
}

// routerTypeID is the one creatable router type (Neutron Router), discovered once per Client.
func (c *Client) routerTypeID(ctx context.Context) (int64, error) {
	return cached(ctx, &c.cache, "router-type", func(ctx context.Context) (int64, error) {
		type item struct {
			ID        int64  `json:"id"`
			Name      string `json:"name"`
			Creatable bool   `json:"creatable"`
		}
		items, err := listAll[item](ctx, c, "/api/network-router-types", "networkRouterTypes", nil)
		if err != nil {
			return 0, err
		}
		var opts []Option
		for _, it := range items {
			if it.Creatable {
				opts = append(opts, Option{ID: it.ID, Name: it.Name})
			}
		}
		t, err := exactlyOne("router type", opts)
		return t.ID, err
	})
}

// routerBody holds the fields create and update both send. Interfaces always go as the full list:
// [] removes them all, so update must never leave them out by accident.
func routerBody(in RouterInput) map[string]any {
	b := map[string]any{
		"description": in.Description,
		"enabled":     in.Enabled,
		"interfaces":  map[string]any{"networkSubnet": map[string]any{"id": append([]int64{}, in.SubnetIDs...)}},
	}
	switch {
	case in.KeepGateway:
	case in.ExternalNetworkID == 0:
		b["externalNetwork"] = nil // null removes the gateway
	default:
		b["externalNetwork"] = map[string]any{"id": in.ExternalNetworkID}
	}
	return b
}

// CreateRouter creates a router and returns its ID. It is still provisioning: WaitForRouter.
func (c *Client) CreateRouter(ctx context.Context, in RouterInput) (int64, error) {
	if in.ProjectID == 0 {
		return 0, fmt.Errorf("a router needs a project")
	}
	p, err := c.Placement(ctx)
	if err != nil {
		return 0, err
	}
	typeID, err := c.routerTypeID(ctx)
	if err != nil {
		return 0, err
	}
	in.KeepGateway = false
	b := routerBody(in)
	b["name"] = in.Name
	b["type"] = map[string]any{"id": typeID}
	b["site"] = map[string]any{"id": p.GroupID}
	b["zone"] = map[string]any{"id": p.CloudID}
	b["poolId"] = in.ProjectID
	var resp struct {
		ID int64 `json:"id"`
	}
	if err := c.do(ctx, http.MethodPost, "/api/networks/routers", nil, map[string]any{"networkRouter": b}, &resp); err != nil {
		return 0, err
	}
	if resp.ID == 0 {
		return 0, fmt.Errorf("MTN Cloud API POST /api/networks/routers: the answer has no router ID")
	}
	return resp.ID, nil
}

// GetRouter reads a router. A missing router is an APIError with IsNotFound.
func (c *Client) GetRouter(ctx context.Context, id int64) (*Router, error) {
	var resp struct {
		NetworkRouter wireRouter `json:"networkRouter"`
	}
	if err := c.get(ctx, "/api/networks/routers/"+itoa(id), nil, &resp); err != nil {
		return nil, err
	}
	return resp.NetworkRouter.router(), nil
}

// FindRouters returns the routers with exactly this name, in any project: the API doesn't echo a
// router's project, so callers tell them apart by their networks.
func (c *Client) FindRouters(ctx context.Context, name string) ([]*Router, error) {
	items, err := listAll[wireRouter](ctx, c, "/api/networks/routers", "networkRouters", url.Values{"name": {name}})
	if err != nil {
		return nil, err
	}
	var out []*Router
	for _, w := range items {
		if w.Name == name {
			out = append(out, w.router())
		}
	}
	return out, nil
}

// ListRouters returns every router the caller can see.
func (c *Client) ListRouters(ctx context.Context) ([]*Router, error) {
	items, err := listAll[wireRouter](ctx, c, "/api/networks/routers", "networkRouters", nil)
	if err != nil {
		return nil, err
	}
	out := make([]*Router, len(items))
	for i, w := range items {
		out[i] = w.router()
	}
	return out, nil
}

// UpdateRouter changes the description, enabled flag, gateway and interfaces. The API refuses a
// new name ("Field name cannot be changed"), so the name is never sent.
func (c *Client) UpdateRouter(ctx context.Context, id int64, in RouterInput) error {
	return c.do(ctx, http.MethodPut, "/api/networks/routers/"+itoa(id), nil, map[string]any{"networkRouter": routerBody(in)}, nil)
}

// DeleteRouter deletes a router. A router that is already gone counts as deleted.
func (c *Client) DeleteRouter(ctx context.Context, id int64) error {
	return ignoreNotFound(c.do(ctx, http.MethodDelete, "/api/networks/routers/"+itoa(id), nil, nil, nil))
}

// WaitForRouter polls a router until it is "ok" or failed, and returns it. A 404 just after the
// create is the platform catching up (rule 13), so it is retried. A failed router is returned with
// no error: the caller reports the status and keeps the router in state.
func (c *Client) WaitForRouter(ctx context.Context, id int64) (*Router, error) {
	return poll(ctx, c, func(ctx context.Context) (*Router, error) { return c.GetRouter(ctx, id) },
		func(r *Router, err error) (bool, error) {
			switch {
			case IsNotFound(err):
				return false, nil
			case err != nil:
				return false, err
			}
			return strings.EqualFold(r.Status, "ok") || RouterFailed(r.Status), nil
		})
}

// WaitForRouterGone polls until a deleted router reads as 404.
func (c *Client) WaitForRouterGone(ctx context.Context, id int64) error {
	_, err := poll(ctx, c, func(ctx context.Context) (*Router, error) { return c.GetRouter(ctx, id) },
		func(_ *Router, err error) (bool, error) {
			if IsNotFound(err) {
				return true, nil
			}
			return false, err
		})
	return err
}

// PoolRouter tells whether a floating IP from pool (an external network, by name) can reach a VM on
// network netID: ok when a router holds the network with that external network as its gateway.
// Otherwise others lists the routers that hold it with another gateway, as "<router> (external
// network <name>)"; none at all means the network has no router with a gateway.
func (c *Client) PoolRouter(ctx context.Context, netID int64, pool string) (ok bool, others []string, err error) {
	routers, err := c.ListRouters(ctx)
	if err != nil {
		return false, nil, err
	}
	for _, rt := range routers {
		if rt.ExternalNetworkID == 0 || !slices.Contains(rt.NetworkIDs, netID) {
			continue
		}
		if strings.EqualFold(rt.ExternalNetworkName, pool) {
			return true, nil, nil
		}
		others = append(others, fmt.Sprintf("%s (external network %s)", rt.Name, rt.ExternalNetworkName))
	}
	return false, others, nil
}

// PoolRouterProblem is PoolRouter's answer as a message for the user, with whether it is an error
// (a router on another external network) or only a warning (no router: one may be made in the same
// apply). An empty message means the pool fits.
func (c *Client) PoolRouterProblem(ctx context.Context, netID int64, pool string) (msg string, isErr bool) {
	ok, others, err := c.PoolRouter(ctx, netID, pool)
	switch {
	case err != nil:
		return "The floating IP pool couldn't be checked against the network's router: " + err.Error() + ".", false
	case ok:
		return "", false
	case len(others) > 0:
		return fmt.Sprintf("Network %d is on router %s, so a floating IP can only come from that external network. Use "+
			"that pool, or change the router's external_network first (in its own apply).", netID, strings.Join(others, ", ")), true
	}
	return fmt.Sprintf("Network %d isn't on an MTN Router with external network %q, so the floating IP can't be attached "+
		"and the apply will fail once the VM is running. Unless this apply creates that router, add one "+
		"(mtncloud_router with external_network = %q and this network) and reference it in depends_on.", netID, pool, pool), false
}
