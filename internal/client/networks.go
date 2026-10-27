package client

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// MTN Networks: POST/GET/PUT/DELETE /api/networks[/{id}]. Shapes were captured as a customer
// (sub-tenant) on MTN Cloud, 2026-10-26, with reads and dry runs: see dev-docs/api-research.md.
// A network is created synchronously in OpenStack together with one subnet of the same CIDR.

// Network is an MTN Network as the API returns it.
type Network struct {
	ID              int64
	Name            string
	Description     string
	ExternalID      string // Neutron UUID
	Status          string
	ProjectID       int64
	ProjectName     string // full platform name, for example "web-prod-AcmeCorp"
	CIDR            string
	Gateway         string
	DNSPrimary      string
	DNSSecondary    string
	DHCP            bool
	AllowIPOverride bool
	IPv6            *NetworkIPv6 // nil when IPv6 is off
	SubnetID        int64        // the subnet OpenStack made with the network; 0 if none yet
}

// NetworkIPv6 is a network's IPv6 settings.
type NetworkIPv6 struct {
	CIDR         string
	Gateway      string
	DNSPrimary   string
	DNSSecondary string
	DHCP         bool
}

// NetworkInput is a network to create, or the new settings of one.
type NetworkInput struct {
	Name            string
	Description     string
	ProjectID       int64 // create only
	CIDR            string
	Gateway         string
	DNSPrimary      string
	DNSSecondary    string
	DHCP            bool
	AllowIPOverride bool
	IPv6            *NetworkIPv6
}

type wireNetwork struct {
	ID                  int64    `json:"id"`
	Name                string   `json:"name"`
	Description         *string  `json:"description"`
	ExternalID          *string  `json:"externalId"`
	Status              string   `json:"status"`
	ZonePool            *wireRef `json:"zonePool"`
	CIDR                *string  `json:"cidr"`
	Gateway             *string  `json:"gateway"`
	DNSPrimary          *string  `json:"dnsPrimary"`
	DNSSecondary        *string  `json:"dnsSecondary"`
	DHCPServer          bool     `json:"dhcpServer"`
	AllowStaticOverride bool     `json:"allowStaticOverride"`
	IPv6Enabled         bool     `json:"ipv6Enabled"`
	CIDRIPv6            *string  `json:"cidrIPv6"`
	GatewayIPv6         *string  `json:"gatewayIPv6"`
	DNSPrimaryIPv6      *string  `json:"dnsPrimaryIPv6"`
	DNSSecondaryIPv6    *string  `json:"dnsSecondaryIPv6"`
	DHCPServerIPv6      bool     `json:"dhcpServerIPv6"`
	Subnets             []struct {
		ID int64 `json:"id"`
	} `json:"subnets"`
}

func (w wireNetwork) network() *Network {
	n := &Network{
		ID: w.ID, Name: w.Name, Description: deref(w.Description), ExternalID: deref(w.ExternalID),
		Status: w.Status, CIDR: deref(w.CIDR), Gateway: deref(w.Gateway), DNSPrimary: deref(w.DNSPrimary),
		DNSSecondary: deref(w.DNSSecondary), DHCP: w.DHCPServer, AllowIPOverride: w.AllowStaticOverride,
	}
	if w.ZonePool != nil {
		n.ProjectID, n.ProjectName = w.ZonePool.ID, w.ZonePool.Name
	}
	if w.IPv6Enabled {
		n.IPv6 = &NetworkIPv6{
			CIDR: deref(w.CIDRIPv6), Gateway: deref(w.GatewayIPv6), DNSPrimary: deref(w.DNSPrimaryIPv6),
			DNSSecondary: deref(w.DNSSecondaryIPv6), DHCP: w.DHCPServerIPv6,
		}
	}
	if len(w.Subnets) > 0 {
		n.SubnetID = w.Subnets[0].ID
	}
	return n
}

// NetworkPlacement is the network type and network server ("Network Service" in the console)
// new MTN Networks are created with.
type NetworkPlacement struct {
	TypeID   int64
	ServerID int64
}

// NetworkPlacement discovers the network type and server from the option lists the console uses,
// once per Client. Customers are offered one of each (OpenStack Private Network, and the server
// named like the cloud); more than one is reported rather than guessed.
func (c *Client) NetworkPlacement(ctx context.Context) (*NetworkPlacement, error) {
	return cached(ctx, &c.cache, "network-placement", func(ctx context.Context) (*NetworkPlacement, error) {
		types, err := c.options(ctx, "creatableNetworkTypes", nil)
		if err != nil {
			return nil, err
		}
		typ, err := exactlyOne("network type", types)
		if err != nil {
			return nil, err
		}
		servers, err := c.options(ctx, "networkServer", url.Values{"networkType.id": {itoa(typ.ID)}})
		if err != nil {
			return nil, err
		}
		server, err := exactlyOne("network service", servers)
		if err != nil {
			return nil, err
		}
		return &NetworkPlacement{TypeID: typ.ID, ServerID: server.ID}, nil
	})
}

// networkBody holds the fields create and update both send.
func networkBody(in NetworkInput) map[string]any {
	b := map[string]any{
		"name":                in.Name,
		"description":         in.Description,
		"gateway":             in.Gateway,
		"dnsPrimary":          in.DNSPrimary,
		"dnsSecondary":        in.DNSSecondary,
		"dhcpServer":          in.DHCP,
		"allowStaticOverride": in.AllowIPOverride,
		"ipv6Enabled":         in.IPv6 != nil,
	}
	if in.IPv6 != nil {
		b["cidrIPv6"] = in.IPv6.CIDR
		b["gatewayIPv6"] = in.IPv6.Gateway
		b["dnsPrimaryIPv6"] = in.IPv6.DNSPrimary
		b["dnsSecondaryIPv6"] = in.IPv6.DNSSecondary
		b["dhcpServerIPv6"] = in.IPv6.DHCP
	}
	return b
}

// CreateNetwork creates an MTN Network and its subnet in a project. The project is always sent:
// left out, the platform puts the network in the tenant's "admin" project.
func (c *Client) CreateNetwork(ctx context.Context, in NetworkInput) (*Network, error) {
	if in.ProjectID == 0 {
		return nil, fmt.Errorf("a network needs a project")
	}
	p, err := c.Placement(ctx)
	if err != nil {
		return nil, err
	}
	np, err := c.NetworkPlacement(ctx)
	if err != nil {
		return nil, err
	}
	b := networkBody(in)
	b["site"] = map[string]any{"id": p.GroupID}
	b["type"] = map[string]any{"id": np.TypeID}
	b["networkServer"] = map[string]any{"id": np.ServerID}
	b["zonePool"] = map[string]any{"id": in.ProjectID}
	b["cidr"] = in.CIDR
	b["ipv4Enabled"] = true
	b["active"] = true
	b["visibility"] = "private"
	var resp struct {
		Network wireNetwork `json:"network"`
	}
	if err := c.do(ctx, http.MethodPost, "/api/networks", nil, map[string]any{"network": b}, &resp); err != nil {
		return nil, err
	}
	return resp.Network.network(), nil
}

// GetNetwork reads a network. A missing network is an APIError with IsNotFound.
func (c *Client) GetNetwork(ctx context.Context, id int64) (*Network, error) {
	var resp struct {
		Network wireNetwork `json:"network"`
	}
	if err := c.get(ctx, "/api/networks/"+itoa(id), nil, &resp); err != nil {
		return nil, err
	}
	return resp.Network.network(), nil
}

// FindNetwork returns the network with exactly this name in the project, or nil.
func (c *Client) FindNetwork(ctx context.Context, name string, projectID int64) (*Network, error) {
	items, err := listAll[wireNetwork](ctx, c, "/api/networks", "networks", url.Values{"name": {name}})
	if err != nil {
		return nil, err
	}
	for _, w := range items {
		if w.Name == name && w.ZonePool != nil && w.ZonePool.ID == projectID {
			return w.network(), nil
		}
	}
	return nil, nil
}

// UpdateNetwork changes the settings that can change in place (not the project or the CIDR).
func (c *Client) UpdateNetwork(ctx context.Context, id int64, in NetworkInput) error {
	return c.do(ctx, http.MethodPut, "/api/networks/"+itoa(id), nil, map[string]any{"network": networkBody(in)}, nil)
}

// DeleteNetwork deletes a network and its subnet. A network that is already gone counts as deleted.
func (c *Client) DeleteNetwork(ctx context.Context, id int64) error {
	return ignoreNotFound(c.do(ctx, http.MethodDelete, "/api/networks/"+itoa(id), nil, nil, nil))
}

// IsInUse reports an OpenStack refusal to delete something still attached elsewhere, for example
// "There are one or more ports still in use on the network".
func IsInUse(err error) bool {
	var apiErr *APIError
	return errors.As(err, &apiErr) && strings.Contains(strings.ToLower(apiErr.Message), "still in use")
}
