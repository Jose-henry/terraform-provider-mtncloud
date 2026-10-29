package loadbalancer

import (
	"cmp"
	"encoding/json"
	"net/netip"
	"slices"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/mtn/terraform-provider-mtncloud/internal/client"
)

// Nested objects are computed only, so their models use plain Go values. Lists are sorted by name,
// then ID, so the platform's ordering never shows as a change.

func str(desc string) schema.StringAttribute {
	return schema.StringAttribute{Computed: true, Description: desc}
}
func num(desc string) schema.Int64Attribute {
	return schema.Int64Attribute{Computed: true, Description: desc}
}
func flag(desc string) schema.BoolAttribute {
	return schema.BoolAttribute{Computed: true, Description: desc}
}
func strs(desc string) schema.ListAttribute {
	return schema.ListAttribute{Computed: true, ElementType: types.StringType, Description: desc}
}

func statusAttributes() map[string]schema.Attribute {
	return map[string]schema.Attribute{
		"enabled":             flag("Whether it is administratively up."),
		"provisioning_status": str("`ACTIVE`, or `PENDING_CREATE`, `PENDING_UPDATE`, `PENDING_DELETE` while a change is applied, or `ERROR`."),
		"operating_status":    str("`ONLINE`, `OFFLINE`, `DEGRADED`, `ERROR` or `NO_MONITOR`."),
	}
}

// --- listener

type listenerModel struct {
	ID                   string   `tfsdk:"id"`
	Name                 string   `tfsdk:"name"`
	Description          string   `tfsdk:"description"`
	Protocol             string   `tfsdk:"protocol"`
	Port                 int64    `tfsdk:"port"`
	ConnectionLimit      int64    `tfsdk:"connection_limit"`
	DefaultPoolID        string   `tfsdk:"default_pool_id"`
	TLSCertificateRef    string   `tfsdk:"tls_certificate_ref"`
	AllowedCIDRs         []string `tfsdk:"allowed_cidrs"`
	ClientDataTimeout    int64    `tfsdk:"client_data_timeout"`
	MemberConnectTimeout int64    `tfsdk:"member_connect_timeout"`
	MemberDataTimeout    int64    `tfsdk:"member_data_timeout"`
	TCPInspectTimeout    int64    `tfsdk:"tcp_inspect_timeout"`
	Enabled              bool     `tfsdk:"enabled"`
	ProvisioningStatus   string   `tfsdk:"provisioning_status"`
	OperatingStatus      string   `tfsdk:"operating_status"`
}

func listenerAttributes() map[string]schema.Attribute {
	return merge(statusAttributes(), map[string]schema.Attribute{
		"id":                     str("Listener ID."),
		"name":                   str("Listener name."),
		"description":            str("Description, or empty."),
		"protocol":               str("`HTTP`, `HTTPS`, `TERMINATED_HTTPS`, `TCP` or `UDP`."),
		"port":                   num("Port it listens on."),
		"connection_limit":       num("Most connections at once; `-1` for no limit."),
		"default_pool_id":        str("ID of the pool it sends traffic to, or empty."),
		"tls_certificate_ref":    str("Reference to the certificate a `TERMINATED_HTTPS` listener uses, or empty."),
		"allowed_cidrs":          strs("Addresses allowed to connect; empty allows any."),
		"client_data_timeout":    num("Milliseconds a client connection may stay idle."),
		"member_connect_timeout": num("Milliseconds to wait for a member to accept a connection."),
		"member_data_timeout":    num("Milliseconds a member connection may stay idle."),
		"tcp_inspect_timeout":    num("Milliseconds to wait for more data to inspect."),
	})
}

func flattenListener(l client.LBListener) listenerModel {
	return listenerModel{
		ID: l.ID, Name: l.Name, Description: l.Description, Protocol: l.Protocol, Port: l.Port,
		ConnectionLimit: l.ConnectionLimit, DefaultPoolID: l.DefaultPoolID, TLSCertificateRef: l.TLSContainerRef,
		AllowedCIDRs: sorted(l.AllowedCIDRs), ClientDataTimeout: l.ClientDataTimeout, MemberConnectTimeout: l.MemberConnTimeout,
		MemberDataTimeout: l.MemberDataTimeout, TCPInspectTimeout: l.TCPInspectTimeout,
		Enabled: l.AdminStateUp, ProvisioningStatus: l.ProvisioningStatus, OperatingStatus: l.OperatingStatus,
	}
}

func flattenListeners(in []client.LBListener) []listenerModel {
	out := make([]listenerModel, 0, len(in))
	for _, l := range in {
		out = append(out, flattenListener(l))
	}
	sortBy(out, func(m listenerModel) (string, string) { return m.Name, m.ID })
	return out
}

// --- member

type memberModel struct {
	ID                 string `tfsdk:"id"`
	Name               string `tfsdk:"name"`
	Address            string `tfsdk:"address"`
	Port               int64  `tfsdk:"port"`
	Weight             int64  `tfsdk:"weight"`
	Backup             bool   `tfsdk:"backup"`
	SubnetID           string `tfsdk:"subnet_id"`
	MonitorAddress     string `tfsdk:"monitor_address"`
	MonitorPort        int64  `tfsdk:"monitor_port"`
	Enabled            bool   `tfsdk:"enabled"`
	ProvisioningStatus string `tfsdk:"provisioning_status"`
	OperatingStatus    string `tfsdk:"operating_status"`
}

func memberAttributes() map[string]schema.Attribute {
	return merge(statusAttributes(), map[string]schema.Attribute{
		"id":              str("Member ID."),
		"name":            str("Member name, or empty."),
		"address":         str("Backend IP address."),
		"port":            num("Backend port."),
		"weight":          num("Share of traffic, 0 to 256; 0 sends it none."),
		"backup":          flag("Whether it gets traffic only when every other member is down."),
		"subnet_id":       str("Subnet the address is on, or empty for the load balancer's own."),
		"monitor_address": str("Address the health monitor checks instead of `address`, or empty."),
		"monitor_port":    num("Port the health monitor checks instead of `port`, or 0."),
	})
}

func flattenMembers(in []client.LBMember) []memberModel {
	out := make([]memberModel, 0, len(in))
	for _, m := range in {
		out = append(out, memberModel{
			ID: m.ID, Name: m.Name, Address: m.Address, Port: m.Port, Weight: m.Weight, Backup: m.Backup,
			SubnetID: m.SubnetID, MonitorAddress: m.MonitorAddress, MonitorPort: m.MonitorPort,
			Enabled: m.AdminStateUp, ProvisioningStatus: m.ProvisioningStatus, OperatingStatus: m.OperatingStatus,
		})
	}
	// By address as a number (10.0.0.4 before 10.0.0.10), then port and ID.
	slices.SortStableFunc(out, func(a, b memberModel) int {
		x, errA := netip.ParseAddr(a.Address)
		y, errB := netip.ParseAddr(b.Address)
		if c := cmp.Compare(boolInt(errA != nil), boolInt(errB != nil)); c != 0 {
			return c // unparsable addresses last
		}
		if c := x.Compare(y); errA == nil && c != 0 {
			return c
		}
		if c := strings.Compare(a.Address, b.Address); c != 0 {
			return c
		}
		if c := cmp.Compare(a.Port, b.Port); c != 0 {
			return c
		}
		return strings.Compare(a.ID, b.ID)
	})
	return out
}

// --- health monitor

type monitorModel struct {
	ID                 string `tfsdk:"id"`
	Name               string `tfsdk:"name"`
	PoolID             string `tfsdk:"pool_id"`
	PoolName           string `tfsdk:"pool_name"`
	Type               string `tfsdk:"type"`
	Delay              int64  `tfsdk:"delay"`
	Timeout            int64  `tfsdk:"timeout"`
	MaxRetries         int64  `tfsdk:"max_retries"`
	MaxRetriesDown     int64  `tfsdk:"max_retries_down"`
	HTTPMethod         string `tfsdk:"http_method"`
	URLPath            string `tfsdk:"url_path"`
	ExpectedCodes      string `tfsdk:"expected_codes"`
	Enabled            bool   `tfsdk:"enabled"`
	ProvisioningStatus string `tfsdk:"provisioning_status"`
	OperatingStatus    string `tfsdk:"operating_status"`
}

func monitorAttributes() map[string]schema.Attribute {
	return merge(statusAttributes(), map[string]schema.Attribute{
		"id":               str("Health monitor ID."),
		"name":             str("Health monitor name, or empty."),
		"pool_id":          str("ID of the pool it checks."),
		"pool_name":        str("Name of the pool it checks."),
		"type":             str("`HTTP`, `HTTPS`, `PING`, `TCP`, `TLS-HELLO` or `UDP-CONNECT`."),
		"delay":            num("Seconds between checks."),
		"timeout":          num("Seconds a check may take."),
		"max_retries":      num("Successful checks in a row before a member is marked up."),
		"max_retries_down": num("Failed checks in a row before a member is marked down."),
		"http_method":      str("HTTP method for `HTTP` and `HTTPS` checks, or empty."),
		"url_path":         str("Path requested by `HTTP` and `HTTPS` checks, or empty."),
		"expected_codes":   str("HTTP status codes that count as up, for example `200` or `200-204`, or empty."),
	})
}

func flattenMonitor(p client.LBPool) *monitorModel {
	h := p.HealthMonitor
	if h == nil || h.ID == "" {
		return nil
	}
	return &monitorModel{
		ID: h.ID, Name: h.Name, PoolID: p.ID, PoolName: p.Name, Type: h.Type, Delay: h.Delay, Timeout: h.Timeout,
		MaxRetries: h.MaxRetries, MaxRetriesDown: h.MaxRetriesDown, HTTPMethod: h.HTTPMethod, URLPath: h.URLPath,
		ExpectedCodes: h.ExpectedCodes, Enabled: h.AdminStateUp, ProvisioningStatus: h.ProvisioningStatus, OperatingStatus: h.OperatingStatus,
	}
}

func flattenMonitors(pools []client.LBPool) []monitorModel {
	out := []monitorModel{}
	for _, p := range pools {
		if m := flattenMonitor(p); m != nil {
			out = append(out, *m)
		}
	}
	sortBy(out, func(m monitorModel) (string, string) { return m.PoolName, m.ID })
	return out
}

// --- pool

type poolModel struct {
	ID                 string        `tfsdk:"id"`
	Name               string        `tfsdk:"name"`
	Description        string        `tfsdk:"description"`
	Protocol           string        `tfsdk:"protocol"`
	Algorithm          string        `tfsdk:"algorithm"`
	SessionPersistence string        `tfsdk:"session_persistence"`
	CookieName         string        `tfsdk:"cookie_name"`
	TLSEnabled         bool          `tfsdk:"tls_enabled"`
	ListenerIDs        []string      `tfsdk:"listener_ids"`
	Members            []memberModel `tfsdk:"members"`
	HealthMonitor      *monitorModel `tfsdk:"health_monitor"`
	Enabled            bool          `tfsdk:"enabled"`
	ProvisioningStatus string        `tfsdk:"provisioning_status"`
	OperatingStatus    string        `tfsdk:"operating_status"`
}

func poolAttributes() map[string]schema.Attribute {
	return merge(statusAttributes(), map[string]schema.Attribute{
		"id":                  str("Pool ID."),
		"name":                str("Pool name."),
		"description":         str("Description, or empty."),
		"protocol":            str("Protocol to the members: `HTTP`, `HTTPS`, `PROXY`, `PROXYV2`, `TCP` or `UDP`."),
		"algorithm":           str("How traffic is shared: `ROUND_ROBIN`, `LEAST_CONNECTIONS` or `SOURCE_IP`."),
		"session_persistence": str("`SOURCE_IP`, `HTTP_COOKIE` or `APP_COOKIE` to keep a client on one member, or empty."),
		"cookie_name":         str("Cookie used by `APP_COOKIE` persistence, or empty."),
		"tls_enabled":         flag("Whether connections to the members use TLS."),
		"listener_ids":        strs("IDs of the listeners that use this pool."),
		"members": schema.ListNestedAttribute{
			Computed: true, Description: "Backends, sorted by address.",
			NestedObject: schema.NestedAttributeObject{Attributes: memberAttributes()},
		},
		"health_monitor": schema.SingleNestedAttribute{
			Computed: true, Description: "The pool's health monitor, or null without one.", Attributes: monitorAttributes(),
		},
	})
}

func flattenPool(p client.LBPool) poolModel {
	m := poolModel{
		ID: p.ID, Name: p.Name, Description: p.Description, Protocol: p.Protocol, Algorithm: p.Algorithm,
		TLSEnabled: p.TLSEnabled, ListenerIDs: listenerIDs(p.Listeners), Members: flattenMembers(p.Members),
		HealthMonitor: flattenMonitor(p), Enabled: p.AdminStateUp, ProvisioningStatus: p.ProvisioningStatus, OperatingStatus: p.OperatingStatus,
	}
	if sp := p.SessionPersistence; sp != nil {
		m.SessionPersistence, m.CookieName = sp.Type, sp.CookieName
	}
	return m
}

func flattenPools(in []client.LBPool) []poolModel {
	out := make([]poolModel, 0, len(in))
	for _, p := range in {
		out = append(out, flattenPool(p))
	}
	sortBy(out, func(m poolModel) (string, string) { return m.Name, m.ID })
	return out
}

// listenerIDs reads the {"id": ...} references a pool carries.
func listenerIDs(refs []json.RawMessage) []string {
	out := []string{}
	for _, r := range refs {
		var ref struct {
			ID string `json:"id"`
		}
		if json.Unmarshal(r, &ref) == nil && ref.ID != "" {
			out = append(out, ref.ID)
		}
	}
	return sorted(out)
}

// --- helpers

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func sorted(in []string) []string {
	out := append([]string{}, in...)
	slices.Sort(out)
	return out
}

func sortBy[T any](items []T, key func(T) (string, string)) {
	slices.SortStableFunc(items, func(a, b T) int {
		a1, a2 := key(a)
		b1, b2 := key(b)
		if c := strings.Compare(strings.ToLower(a1), strings.ToLower(b1)); c != 0 {
			return c
		}
		return strings.Compare(a2, b2)
	})
}
