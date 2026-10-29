package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/url"
	"strconv"
	"strings"
)

// The lookups below use the endpoints the console's own provisioning wizard calls, because a
// customer role (for example "Customer Admin User") gets 403 on the admin endpoints
// (/api/zones, /api/zones/{id}/resource-pools, /api/service-plans). Captured with the CLI as a
// sub-tenant user, 2026-10-26: see dev-docs/api-research.md.

// WhoAmI is the calling user's tenant.
type WhoAmI struct {
	AccountID   int64
	AccountName string // the tenant name; projects are named "<project>-<AccountName>"
	IsMaster    bool
}

// WhoAmI returns the caller's tenant. It is fetched once per Client.
func (c *Client) WhoAmI(ctx context.Context) (*WhoAmI, error) {
	return cached(ctx, &c.cache, "whoami", func(ctx context.Context) (*WhoAmI, error) {
		var resp struct {
			User struct {
				Account struct {
					ID   int64  `json:"id"`
					Name string `json:"name"`
				} `json:"account"`
			} `json:"user"`
			IsMasterAccount bool `json:"isMasterAccount"` // absent (false) for sub-tenants
		}
		if err := c.get(ctx, "/api/whoami", nil, &resp); err != nil {
			return nil, err
		}
		return &WhoAmI{AccountID: resp.User.Account.ID, AccountName: resp.User.Account.Name, IsMaster: resp.IsMasterAccount}, nil
	})
}

// Placement is where the caller provisions: the cloud and the group the console offers them.
type Placement struct {
	CloudID   int64
	CloudName string
	GroupID   int64
	GroupName string
}

// Placement discovers the caller's cloud and group. MTN Cloud offers each tenant one group and one
// OpenStack cloud (MTNNG_CLOUD_AZ_1). More than one is reported as an error rather than guessed.
func (c *Client) Placement(ctx context.Context) (*Placement, error) {
	return cached(ctx, &c.cache, "placement", func(ctx context.Context) (*Placement, error) {
		groups, err := c.options(ctx, "groups", nil)
		if err != nil {
			return nil, err
		}
		group, err := pick("group", groups, c.cfg.Defaults.Group)
		if err != nil {
			return nil, placementError{err}
		}
		clouds, err := c.options(ctx, "clouds", url.Values{"groupId": {itoa(group.ID)}})
		if err != nil {
			return nil, err
		}
		cloud, err := pick("cloud", clouds, c.cfg.Defaults.Cloud)
		if err != nil {
			return nil, placementError{err}
		}
		return &Placement{CloudID: cloud.ID, CloudName: cloud.Name, GroupID: group.ID, GroupName: group.Name}, nil
	})
}

// Option is one entry of an /api/options/<source> list.
type Option struct {
	ID         int64
	Name       string
	ExternalID string // OpenStack UUID, when the source has one
}

// rawOption accepts the option shapes the platform uses: {"name","value"} with a numeric value,
// or {"id","name","value":"<uuid or pool-N>","externalId"}.
type rawOption struct {
	ID         *int64 `json:"id"`
	Name       string `json:"name"`
	Value      any    `json:"value"`
	ExternalID string `json:"externalId"`
	IsGroup    bool   `json:"isGroup"` // a section header in grouped lists; never a real item
}

func (r rawOption) option() (Option, bool) {
	if r.IsGroup {
		return Option{}, false
	}
	o := Option{Name: r.Name, ExternalID: r.ExternalID}
	switch {
	case r.ID != nil:
		o.ID = *r.ID
		// Security groups carry the Neutron UUID in "value"; pools use "pool-<id>", which isn't one.
		if s, ok := r.Value.(string); ok && o.ExternalID == "" && !strings.HasPrefix(s, "pool-") {
			o.ExternalID = s
		}
	default:
		switch v := r.Value.(type) {
		case float64:
			o.ID = int64(v)
		case string: // some installs send numeric option values as strings
			n, err := strconv.ParseInt(v, 10, 64)
			if err != nil {
				return Option{}, false
			}
			o.ID = n
		default:
			return Option{}, false
		}
	}
	return o, true
}

func (c *Client) options(ctx context.Context, source string, q url.Values) ([]Option, error) {
	key := "options/" + source + "?" + q.Encode()
	return cached(ctx, &c.cache, key, func(ctx context.Context) ([]Option, error) {
		var resp struct {
			Data []rawOption `json:"data"`
		}
		if err := c.get(ctx, "/api/options/"+source, q, &resp); err != nil {
			return nil, err
		}
		out := make([]Option, 0, len(resp.Data))
		for _, r := range resp.Data {
			if o, ok := r.option(); ok {
				out = append(out, o)
			}
		}
		return out, nil
	})
}

// pick returns the group or cloud named want (case ignored), or, with no name, the only one there is.
func pick(kind string, opts []Option, want string) (Option, error) {
	if want == "" {
		o, err := exactlyOne(kind, opts)
		if err != nil && len(opts) > 1 {
			err = fmt.Errorf("%w. Choose one with %s in the provider block, or the MTNCLOUD_%s environment variable", err, kind, strings.ToUpper(kind))
		}
		return o, err
	}
	names := make([]string, len(opts))
	for i, o := range opts {
		if strings.EqualFold(o.Name, want) {
			return o, nil
		}
		names[i] = o.Name
	}
	return Option{}, &NoMatchError{fmt.Sprintf("no %s named %q. Yours: %s", kind, want, list(names))}
}

func exactlyOne(kind string, opts []Option) (Option, error) {
	switch len(opts) {
	case 1:
		return opts[0], nil
	case 0:
		return Option{}, fmt.Errorf("your MTN Cloud account has no %s to provision into. Ask your tenant administrator for access", kind)
	}
	names := make([]string, len(opts))
	for i, o := range opts {
		names[i] = o.Name
	}
	return Option{}, fmt.Errorf("your MTN Cloud account has %d %ss (%s); this provider supports exactly one", len(opts), kind, strings.Join(names, ", "))
}

// Projects lists the projects (OpenStack resource pools) the caller can provision into. Names are
// the full platform names, for example "web-prod-AcmeCorp"; shared pools such as "admin" have no
// tenant suffix.
func (c *Client) Projects(ctx context.Context) ([]Option, error) {
	p, err := c.Placement(ctx)
	if err != nil {
		return nil, err
	}
	return c.ProjectsAt(ctx, p)
}

// ProjectsAt lists the projects of one placement's cloud.
func (c *Client) ProjectsAt(ctx context.Context, p *Placement) ([]Option, error) {
	return c.options(ctx, "zonePools", url.Values{"zoneId": {itoa(p.CloudID)}})
}

// DefaultsFor is Defaults with the group filled in, when none is set, from the caller's only group
// (an MTN Cloud tenant normally has one). With several groups, or when the lookup fails, the group
// stays empty and the resource asks for one.
func (c *Client) DefaultsFor(ctx context.Context) Defaults {
	d := c.cfg.Defaults
	if d.Group == "" {
		if groups, err := c.Groups(ctx); err == nil && len(groups) == 1 {
			d.Group = groups[0].Name
		}
	}
	return d
}

// Groups lists the groups the caller can provision into, for example "MTNNG_CLOUD_AZ_1".
func (c *Client) Groups(ctx context.Context) ([]Option, error) {
	return c.options(ctx, "groups", nil)
}

// placementError is a group or cloud that can't be resolved (provider settings or env vars), which
// surfaces through a project lookup: ProjectErrorTitle tells the two apart.
type placementError struct{ err error }

func (e placementError) Error() string { return e.err.Error() }
func (e placementError) Unwrap() error { return e.err }

// ProjectErrorTitle is the title for an error from a project lookup: "Unknown group or cloud" when
// the group or cloud couldn't be resolved first, otherwise "Unknown project".
func ProjectErrorTitle(err error) string {
	if errors.As(err, new(placementError)) {
		return "Unknown group or cloud"
	}
	return "Unknown project"
}

// PlacementForGroup is the placement of a named group: the group and its one OpenStack cloud.
// Case is ignored, as the console does.
func (c *Client) PlacementForGroup(ctx context.Context, name string) (*Placement, error) {
	groups, err := c.Groups(ctx)
	if err != nil {
		return nil, err
	}
	names := make([]string, len(groups))
	for i, g := range groups {
		names[i] = g.Name
		if !strings.EqualFold(g.Name, name) {
			continue
		}
		clouds, err := c.options(ctx, "clouds", url.Values{"groupId": {itoa(g.ID)}})
		if err != nil {
			return nil, err
		}
		cloud, err := pick("cloud", clouds, c.cfg.Defaults.Cloud)
		if err != nil {
			return nil, placementError{err}
		}
		return &Placement{CloudID: cloud.ID, CloudName: cloud.Name, GroupID: g.ID, GroupName: g.Name}, nil
	}
	return nil, fmt.Errorf("no group named %q. Your groups: %s", name, list(names))
}

// SecurityGroups lists the security groups placed in a project (its synced "default" included).
func (c *Client) SecurityGroups(ctx context.Context, projectID int64) ([]Option, error) {
	p, err := c.Placement(ctx)
	if err != nil {
		return nil, err
	}
	return c.SecurityGroupsAt(ctx, p, projectID)
}

// SecurityGroupsAt lists the security groups placed in a project of one placement's cloud.
func (c *Client) SecurityGroupsAt(ctx context.Context, p *Placement, projectID int64) ([]Option, error) {
	return c.options(ctx, "zoneSecurityGroup", url.Values{"zoneId": {itoa(p.CloudID)}, "poolId": {itoa(projectID)}})
}

// ExternalNetworks lists the networks an MTN Router can use as its external (internet) side.
func (c *Client) ExternalNetworks(ctx context.Context) ([]Option, error) {
	p, err := c.Placement(ctx)
	if err != nil {
		return nil, err
	}
	return c.options(ctx, "openstackExternalNetworks", url.Values{"zoneId": {itoa(p.CloudID)}})
}

// Datastores lists the storage datastores (OpenStack Cinder volume types) a project can use for
// volumes, for example "__DEFAULT__". New datastores appear here without provider changes.
func (c *Client) Datastores(ctx context.Context, projectID int64) ([]Option, error) {
	p, err := c.Placement(ctx)
	if err != nil {
		return nil, err
	}
	return c.DatastoresAt(ctx, p, projectID)
}

// DatastoresAt lists a project's datastores for one placement.
func (c *Client) DatastoresAt(ctx context.Context, p *Placement, projectID int64) ([]Option, error) {
	return c.options(ctx, "datastores", url.Values{
		"zoneId": {itoa(p.CloudID)}, "siteId": {itoa(p.GroupID)}, "resourcePoolId": {itoa(projectID)},
	})
}

// InstanceType is a provisionable type: an OS image (category "os") or an app type.
type InstanceType struct {
	ID       int64
	Code     string
	Name     string
	Category string
	// LayoutID is the OpenStack layout. Every MTN instance type has exactly one; 0 if none.
	LayoutID int64
}

// InstanceTypes lists every instance type visible to the caller.
func (c *Client) InstanceTypes(ctx context.Context) ([]InstanceType, error) {
	return cached(ctx, &c.cache, "instance-types", func(ctx context.Context) ([]InstanceType, error) {
		type layout struct {
			ID                int64  `json:"id"`
			ProvisionTypeCode string `json:"provisionTypeCode"`
		}
		type item struct {
			ID       int64    `json:"id"`
			Code     string   `json:"code"`
			Name     string   `json:"name"`
			Category string   `json:"category"`
			Layouts  []layout `json:"instanceTypeLayouts"`
		}
		items, err := listAll[item](ctx, c, "/api/instance-types", "instanceTypes", nil)
		if err != nil {
			return nil, err
		}
		out := make([]InstanceType, 0, len(items))
		for _, it := range items {
			t := InstanceType{ID: it.ID, Code: it.Code, Name: it.Name, Category: it.Category}
			for _, l := range it.Layouts {
				if l.ProvisionTypeCode == "openstack" {
					t.LayoutID = l.ID
					break
				}
			}
			out = append(out, t)
		}
		return out, nil
	})
}

// Plan is a size (OpenStack flavor) offered in one project for one layout.
type Plan struct {
	ID          int64
	Name        string // for example "G2S4"
	Code        string
	Cores       int64
	MemoryBytes int64
	RootBytes   int64
}

// Plans lists the sizes a project offers for a layout. Each project has its own copy of every
// flavor, so plan IDs differ between projects: always look plans up per project.
func (c *Client) Plans(ctx context.Context, projectID, layoutID int64) ([]Plan, error) {
	p, err := c.Placement(ctx)
	if err != nil {
		return nil, err
	}
	return c.PlansAt(ctx, p, projectID, layoutID)
}

// PlansAt lists the sizes a project offers for a layout, for one placement.
func (c *Client) PlansAt(ctx context.Context, p *Placement, projectID, layoutID int64) ([]Plan, error) {
	q := url.Values{"zoneId": {itoa(p.CloudID)}, "siteId": {itoa(p.GroupID)}, "layoutId": {itoa(layoutID)}, "resourcePoolId": {itoa(projectID)}}
	return cached(ctx, &c.cache, "service-plans?"+q.Encode(), func(ctx context.Context) ([]Plan, error) {
		var resp struct {
			Plans []struct {
				ID         int64  `json:"id"`
				Name       string `json:"name"`
				Code       string `json:"code"`
				MaxCores   int64  `json:"maxCores"`
				MaxMemory  int64  `json:"maxMemory"`
				MaxStorage int64  `json:"maxStorage"`
			} `json:"plans"`
		}
		if err := c.get(ctx, "/api/instances/service-plans", q, &resp); err != nil {
			return nil, err
		}
		out := make([]Plan, 0, len(resp.Plans))
		for _, x := range resp.Plans {
			out = append(out, Plan{ID: x.ID, Name: x.Name, Code: x.Code, Cores: x.MaxCores, MemoryBytes: x.MaxMemory, RootBytes: x.MaxStorage})
		}
		return out, nil
	})
}

// CatalogItem is a service a customer can order from the catalog.
type CatalogItem struct {
	ID          int64
	Name        string
	Description string
	Type        string // "instance", "blueprint" or "workflow"
	Featured    bool
}

// CatalogItems lists the catalog (the customer-facing view, not the admin library).
func (c *Client) CatalogItems(ctx context.Context) ([]CatalogItem, error) {
	return cached(ctx, &c.cache, "catalog-types", func(ctx context.Context) ([]CatalogItem, error) {
		type item struct {
			ID          int64  `json:"id"`
			Name        string `json:"name"`
			Description string `json:"description"`
			Type        string `json:"type"`
			Featured    bool   `json:"featured"`
		}
		items, err := listAll[item](ctx, c, "/api/catalog/types", "catalogItemTypes", nil)
		if err != nil {
			return nil, err
		}
		out := make([]CatalogItem, len(items))
		for i, x := range items {
			out[i] = CatalogItem(x)
		}
		return out, nil
	})
}

const pageSize = 200

// listAll reads every page of a paginated list endpoint ({"<key>": [...], "meta": {...}}).
func listAll[T any](ctx context.Context, c *Client, path, key string, q url.Values) ([]T, error) {
	var all []T
	for offset := 0; ; offset += pageSize {
		page := url.Values{}
		maps.Copy(page, q)
		page.Set("max", strconv.Itoa(pageSize))
		page.Set("offset", strconv.Itoa(offset))
		var resp map[string]json.RawMessage
		if err := c.get(ctx, path, page, &resp); err != nil {
			return nil, err
		}
		raw, ok := resp[key]
		if !ok {
			return nil, fmt.Errorf("MTN Cloud API GET %s: response has no %q list", path, key)
		}
		var items []T
		if err := json.Unmarshal(raw, &items); err != nil {
			return nil, fmt.Errorf("MTN Cloud API GET %s: unexpected %q list (%w)", path, key, err)
		}
		all = append(all, items...)
		// Stop on meta.total when the API sends it (the server may cap "max" below pageSize),
		// otherwise on a short or empty page.
		var meta struct {
			Total *int `json:"total"`
		}
		if m, ok := resp["meta"]; ok && json.Unmarshal(m, &meta) == nil && meta.Total != nil {
			if len(all) >= *meta.Total || len(items) == 0 {
				return all, nil
			}
			continue
		}
		if len(items) < pageSize {
			return all, nil
		}
	}
}

// NamedValue is an option whose value is a code, not an ID, for example an environment.
type NamedValue struct {
	Name  string
	Value string
}

// Environments lists the environments an instance can be tagged with, for example "Production"
// (code "production").
func (c *Client) Environments(ctx context.Context) ([]NamedValue, error) {
	return cached(ctx, &c.cache, "options/environments", func(ctx context.Context) ([]NamedValue, error) {
		var resp struct {
			Data []struct {
				Name  string `json:"name"`
				Value any    `json:"value"`
			} `json:"data"`
		}
		if err := c.get(ctx, "/api/options/environments", nil, &resp); err != nil {
			return nil, err
		}
		out := make([]NamedValue, 0, len(resp.Data))
		for _, d := range resp.Data {
			out = append(out, NamedValue{Name: d.Name, Value: fmt.Sprint(d.Value)})
		}
		return out, nil
	})
}

// NetworkDomain is a DNS domain instances can join, for example "example.com".
type NetworkDomain struct {
	ID   int64
	Name string
}

// NetworkDomains lists the network domains visible to the caller.
func (c *Client) NetworkDomains(ctx context.Context) ([]NetworkDomain, error) {
	return cached(ctx, &c.cache, "network-domains", func(ctx context.Context) ([]NetworkDomain, error) {
		type item struct {
			ID   int64  `json:"id"`
			Name string `json:"name"`
		}
		items, err := listAll[item](ctx, c, "/api/networks/domains", "networkDomains", nil)
		if err != nil {
			return nil, err
		}
		out := make([]NetworkDomain, len(items))
		for i, x := range items {
			out[i] = NetworkDomain(x)
		}
		return out, nil
	})
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }
