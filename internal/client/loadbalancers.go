package client

import (
	"context"
	"encoding/json"
	"fmt"
)

// LoadBalancerSummary is one row of a network's load balancer list.
type LoadBalancerSummary struct {
	ID                 string `json:"id"`
	Name               string `json:"name"`
	Description        string `json:"description"`
	ProvisioningStatus string `json:"provisioning_status"`
	OperatingStatus    string `json:"operating_status"`
	VIPAddress         string `json:"vip_address"`
	VIPSubnetID        string `json:"vip_subnet_id"`
	VIPNetworkID       string `json:"vip_network_id"`
	// FloatingIP is the public address on the VIP. Empty with FloatingIPKnown false means it
	// could not be checked, not that there is none.
	FloatingIP      string `json:"vip_floating"`
	FloatingIPKnown bool   `json:"vip_floating_known"`
	MembersCount    int64  `json:"membersCount"`
	CreatedAt       string `json:"created_at"`
}

// LoadBalancerList is a network's load balancers, and whether the tenant may change them (it owns
// the network; a shared network is read-only).
type LoadBalancerList struct {
	LoadBalancers []LoadBalancerSummary `json:"loadbalancers"`
	Total         int64                 `json:"total"`
	CanManage     bool                  `json:"canManage"`
}

// LoadBalancer is one load balancer with its listeners and pools, as its network's editor reads it.
type LoadBalancer struct {
	ID                 string       `json:"id"`
	Name               string       `json:"name"`
	Description        string       `json:"description"`
	ProvisioningStatus string       `json:"provisioning_status"`
	OperatingStatus    string       `json:"operating_status"`
	AdminStateUp       bool         `json:"admin_state_up"`
	VIPAddress         string       `json:"vip_address"`
	VIPSubnetID        string       `json:"vip_subnet_id"`
	VIPSubnet          string       `json:"vip_subnet_display"` // "Name (CIDR)"
	VIPNetworkID       string       `json:"vip_network_id"`
	VIPPortID          string       `json:"vip_port_id"`
	Tags               []string     `json:"tags"`
	CreatedAt          string       `json:"created_at"`
	UpdatedAt          string       `json:"updated_at"`
	Listeners          []LBListener `json:"listeners"`
	Pools              []LBPool     `json:"pools"`
}

// LBListener is a port the load balancer listens on.
type LBListener struct {
	ID                 string   `json:"id"`
	Name               string   `json:"name"`
	Description        string   `json:"description"`
	ProvisioningStatus string   `json:"provisioning_status"`
	OperatingStatus    string   `json:"operating_status"`
	AdminStateUp       bool     `json:"admin_state_up"`
	Protocol           string   `json:"protocol"`
	Port               int64    `json:"protocol_port"`
	ConnectionLimit    int64    `json:"connection_limit"`
	DefaultPoolID      string   `json:"default_pool_id"`
	TLSContainerRef    string   `json:"default_tls_container_ref"`
	SNIContainerRefs   []string `json:"sni_container_refs"`
	AllowedCIDRs       []string `json:"allowed_cidrs"`
	ClientDataTimeout  int64    `json:"timeout_client_data"`
	MemberConnTimeout  int64    `json:"timeout_member_connect"`
	MemberDataTimeout  int64    `json:"timeout_member_data"`
	TCPInspectTimeout  int64    `json:"timeout_tcp_inspect"`
	Tags               []string `json:"tags"`
}

// LBPool is a group of members traffic is shared across.
type LBPool struct {
	ID                 string            `json:"id"`
	Name               string            `json:"name"`
	Description        string            `json:"description"`
	ProvisioningStatus string            `json:"provisioning_status"`
	OperatingStatus    string            `json:"operating_status"`
	AdminStateUp       bool              `json:"admin_state_up"`
	Protocol           string            `json:"protocol"`
	Algorithm          string            `json:"lb_algorithm"`
	SessionPersistence *LBPersistence    `json:"session_persistence"`
	TLSEnabled         bool              `json:"tls_enabled"`
	Members            []LBMember        `json:"members"`
	HealthMonitor      *LBHealthMonitor  `json:"healthmonitor"`
	Listeners          []json.RawMessage `json:"listeners"`
	Tags               []string          `json:"tags"`
}

// LBPersistence keeps a client on one member.
type LBPersistence struct {
	Type       string `json:"type"`
	CookieName string `json:"cookie_name"`
}

// LBMember is one backend address and port in a pool.
type LBMember struct {
	ID                 string `json:"id"`
	Name               string `json:"name"`
	ProvisioningStatus string `json:"provisioning_status"`
	OperatingStatus    string `json:"operating_status"`
	AdminStateUp       bool   `json:"admin_state_up"`
	Address            string `json:"address"`
	Port               int64  `json:"protocol_port"`
	Weight             int64  `json:"weight"`
	Backup             bool   `json:"backup"`
	SubnetID           string `json:"subnet_id"`
	MonitorAddress     string `json:"monitor_address"`
	MonitorPort        int64  `json:"monitor_port"`
}

// LBHealthMonitor checks a pool's members.
type LBHealthMonitor struct {
	ID                 string `json:"id"`
	Name               string `json:"name"`
	Type               string `json:"type"`
	Delay              int64  `json:"delay"`
	Timeout            int64  `json:"timeout"`
	MaxRetries         int64  `json:"max_retries"`
	MaxRetriesDown     int64  `json:"max_retries_down"`
	HTTPMethod         string `json:"http_method"`
	URLPath            string `json:"url_path"`
	ExpectedCodes      string `json:"expected_codes"`
	AdminStateUp       bool   `json:"admin_state_up"`
	ProvisioningStatus string `json:"provisioning_status"`
	OperatingStatus    string `json:"operating_status"`
}

// LBFloatingIPOptions lists the pools a project may take a public address from, and the addresses
// it already holds that are not attached.
type LBFloatingIPOptions struct {
	Pools []struct {
		Name       string `json:"name"`
		ExternalID string `json:"externalId"`
	} `json:"floatingIpPools"`
	Available []struct {
		ID      string `json:"id"`
		Address string `json:"address"`
	} `json:"availableFloatingIps"`
}

// LBListFilter narrows a network's list. Zero values don't filter.
type LBListFilter struct {
	Name   string // exact
	Status string // provisioning or operating status, any case
}

// ListLoadBalancers lists the load balancers on a network. A failed read is an error, never an
// empty list.
func (c *Client) ListLoadBalancers(ctx context.Context, networkID int64, f LBListFilter) (*LoadBalancerList, error) {
	in := map[string]any{"networkId": itoa(networkID)}
	if f.Name != "" {
		in["name"] = f.Name
	}
	if f.Status != "" {
		in["status"] = f.Status
	}
	raw, err := c.runLB(ctx, "list", in)
	if err != nil {
		return nil, err
	}
	var out LoadBalancerList
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("load balancer list: unexpected answer (%w)", err)
	}
	// A failed read is an error, never an empty list.
	var has struct {
		LoadBalancers *json.RawMessage `json:"loadbalancers"`
	}
	if json.Unmarshal(raw, &has) != nil || has.LoadBalancers == nil {
		return nil, fmt.Errorf("load balancer list: the answer has no list of load balancers")
	}
	return &out, nil
}

// GetLoadBalancer reads one load balancer on a network, by ID, or by name when id is empty. A
// name must match exactly one; the plugin names the IDs when it doesn't.
func (c *Client) GetLoadBalancer(ctx context.Context, networkID int64, id, name string) (*LoadBalancer, bool, error) {
	in := map[string]any{"networkId": itoa(networkID)}
	if id != "" {
		in["lbId"] = id
	} else {
		in["name"] = name
	}
	raw, err := c.runLB(ctx, "read", in)
	if err != nil {
		return nil, false, err
	}
	var out struct {
		LoadBalancer *LoadBalancer `json:"loadbalancer"`
		CanManage    bool          `json:"canManage"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, false, fmt.Errorf("load balancer read: unexpected answer (%w)", err)
	}
	if out.LoadBalancer == nil {
		return nil, false, fmt.Errorf("load balancer read: the answer has no load balancer")
	}
	return out.LoadBalancer, out.CanManage, nil
}

// LoadBalancerSnapshot is GetLoadBalancer read once per provider instance and key: every data source
// about one load balancer (its listeners, pools, members, monitors) shares one console job, which
// takes several seconds. Data sources and the resources' reads use it; every write drops its
// network's entries (lbChanged), so a read after a change is fresh.
func (c *Client) LoadBalancerSnapshot(ctx context.Context, networkID int64, id, name string) (*LoadBalancer, bool, error) {
	type snap struct {
		lb        *LoadBalancer
		canManage bool
	}
	s, err := cached(ctx, &c.cache, fmt.Sprintf("lb-read:%d:%s:%s", networkID, id, name), func(ctx context.Context) (snap, error) {
		lb, canManage, err := c.GetLoadBalancer(ctx, networkID, id, name)
		return snap{lb, canManage}, err
	})
	return s.lb, s.canManage, err
}

// LBFloatingIPChoices lists the floating IP pools and held addresses for a network's load balancers.
func (c *Client) LBFloatingIPChoices(ctx context.Context, networkID int64) (*LBFloatingIPOptions, error) {
	raw, err := c.runLB(ctx, "fip-options", map[string]any{"networkId": itoa(networkID)})
	if err != nil {
		return nil, err
	}
	var out LBFloatingIPOptions
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("load balancer floating IP options: unexpected answer (%w)", err)
	}
	var has struct {
		Pools *json.RawMessage `json:"floatingIpPools"`
	}
	if json.Unmarshal(raw, &has) != nil || has.Pools == nil {
		return nil, fmt.Errorf("load balancer floating IP options: the answer has no list of pools")
	}
	return &out, nil
}
