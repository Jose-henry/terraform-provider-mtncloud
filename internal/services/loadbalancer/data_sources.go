package loadbalancer

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework-validators/datasourcevalidator"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/mtn/terraform-provider-mtncloud/internal/client"
)

// dataSource is one load balancer data source, described by a spec. They differ only in their
// attributes and in what they pick out of a load balancer, so one type serves them all.
type dataSource struct {
	base
	spec spec
}

type spec struct {
	typeName   string // without the provider prefix
	about      string
	attributes func() map[string]schema.Attribute
	validators func() []datasource.ConfigValidator
	// read returns the state to set: a struct whose tfsdk tags cover every attribute.
	read func(ctx context.Context, b *base, cfg tfsdk.Config, diags *diag.Diagnostics) any
}

func (d *dataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_" + d.spec.typeName
}

func (d *dataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{Description: d.spec.about, Attributes: d.spec.attributes()}
}

func (d *dataSource) ConfigValidators(context.Context) []datasource.ConfigValidator {
	if d.spec.validators == nil {
		return nil
	}
	return d.spec.validators()
}

func (d *dataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	if !d.ready(&resp.Diagnostics) {
		return
	}
	state := d.spec.read(ctx, &d.base, req.Config, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
}

// selector is what the data sources about a load balancer's parts were given.
type selector struct {
	networkID int64
	id, name  types.String
}

func readSelector(ctx context.Context, cfg tfsdk.Config, idAttr, nameAttr string, diags *diag.Diagnostics) selector {
	var s selector
	var net types.Int64
	diags.Append(cfg.GetAttribute(ctx, path.Root("network_id"), &net)...)
	diags.Append(cfg.GetAttribute(ctx, path.Root(idAttr), &s.id)...)
	diags.Append(cfg.GetAttribute(ctx, path.Root(nameAttr), &s.name)...)
	s.networkID = net.ValueInt64()
	return s
}

// readPart reads the load balancer named by load_balancer_id / load_balancer_name.
func readPart(ctx context.Context, b *base, cfg tfsdk.Config, diags *diag.Diagnostics) (*client.LoadBalancer, int64) {
	s := readSelector(ctx, cfg, "load_balancer_id", "load_balancer_name", diags)
	if diags.HasError() {
		return nil, 0
	}
	lb, _ := b.readLB(ctx, s.networkID, s.id, s.name, "load_balancer_id", "load_balancer_name", diags)
	return lb, s.networkID
}

// pickInput reads an optional <attr>_id / <attr>_name pair (or id / name when attr is "").
func pickInput(ctx context.Context, cfg tfsdk.Config, attr string, diags *diag.Diagnostics) (id, name types.String) {
	idAttr, nameAttr := "id", "name"
	if attr != "" {
		idAttr, nameAttr = attr+"_id", attr+"_name"
	}
	diags.Append(cfg.GetAttribute(ctx, path.Root(idAttr), &id)...)
	diags.Append(cfg.GetAttribute(ctx, path.Root(nameAttr), &name)...)
	return id, name
}

// partOf is the selector part of every state about a load balancer's parts.
type partOf struct {
	NetworkID        int64  `tfsdk:"network_id"`
	LoadBalancerID   string `tfsdk:"load_balancer_id"`
	LoadBalancerName string `tfsdk:"load_balancer_name"`
}

func partOfLB(net int64, lb *client.LoadBalancer) partOf {
	return partOf{NetworkID: net, LoadBalancerID: lb.ID, LoadBalancerName: lb.Name}
}

// pickAttributes are the optional+computed <what>_id / <what>_name (or id / name) a singular data
// source picks one item by.
func pickAttributes(attr, what string) map[string]schema.Attribute {
	idAttr, nameAttr := "id", "name"
	if attr != "" {
		idAttr, nameAttr = attr+"_id", attr+"_name"
	}
	return map[string]schema.Attribute{
		// Optional+Computed: give one, and the other is filled in.
		idAttr:   schema.StringAttribute{Optional: true, Computed: true, Description: "The " + what + "'s ID. Give this or `" + nameAttr + "`."},
		nameAttr: schema.StringAttribute{Optional: true, Computed: true, Description: "The " + what + "'s name, matched exactly. Give this or `" + idAttr + "`."},
	}
}

func pickValidators(attr string) func() []datasource.ConfigValidator {
	idAttr, nameAttr := "id", "name"
	if attr != "" {
		idAttr, nameAttr = attr+"_id", attr+"_name"
	}
	return func() []datasource.ConfigValidator {
		return append(lbSelectorValidators(), datasourcevalidator.ExactlyOneOf(path.MatchRoot(idAttr), path.MatchRoot(nameAttr)))
	}
}

func nested(desc string, attrs map[string]schema.Attribute) schema.ListNestedAttribute {
	return schema.ListNestedAttribute{Computed: true, Description: desc, NestedObject: schema.NestedAttributeObject{Attributes: attrs}}
}

// --- mtncloud_load_balancers

type summaryModel struct {
	ID                 string `tfsdk:"id"`
	Name               string `tfsdk:"name"`
	Description        string `tfsdk:"description"`
	VIPAddress         string `tfsdk:"vip_address"`
	VIPSubnetID        string `tfsdk:"vip_subnet_id"`
	FloatingIP         string `tfsdk:"floating_ip"`
	FloatingIPChecked  bool   `tfsdk:"floating_ip_checked"`
	MemberCount        int64  `tfsdk:"member_count"`
	CreatedAt          string `tfsdk:"created_at"`
	ProvisioningStatus string `tfsdk:"provisioning_status"`
	OperatingStatus    string `tfsdk:"operating_status"`
}

type listState struct {
	NetworkID     int64          `tfsdk:"network_id"`
	Name          types.String   `tfsdk:"name"`
	Status        types.String   `tfsdk:"status"`
	CanManage     bool           `tfsdk:"can_manage"`
	LoadBalancers []summaryModel `tfsdk:"load_balancers"`
}

func newLoadBalancers() datasource.DataSource {
	return &dataSource{spec: spec{
		typeName: "load_balancers",
		about: "Lists the load balancers on an MTN Network, including ones made in the console, sorted by name. Each read runs a " +
			"console job, so it takes a few seconds. For one load balancer's listeners, pools and members, use `mtncloud_load_balancer`.",
		attributes: func() map[string]schema.Attribute {
			return map[string]schema.Attribute{
				"network_id": networkIDAttribute(),
				"name":       schema.StringAttribute{Optional: true, Description: "Only return the load balancers with this exact name."},
				"status": schema.StringAttribute{
					Optional:    true,
					Description: "Only return load balancers in this provisioning or operating status, for example `ACTIVE` or `ONLINE` (case ignored).",
				},
				"can_manage": flag("Whether your tenant owns the network, so it may change its load balancers. On a shared network it may only read them."),
				"load_balancers": nested("The load balancers, sorted by name.", merge(statusAttributes(), map[string]schema.Attribute{
					"enabled":             nil, // list rows don't carry it
					"id":                  str("Load balancer ID."),
					"name":                str("Load balancer name."),
					"description":         str("Description, or empty."),
					"vip_address":         str("Private address it receives traffic on."),
					"vip_subnet_id":       str("Subnet of that address."),
					"floating_ip":         str("Public IP attached to it, or empty."),
					"floating_ip_checked": flag("Whether the public IP could be checked. When false, an empty `floating_ip` means unknown, not none."),
					"member_count":        num("Backends across all its pools."),
					"created_at":          str("When it was created (RFC 3339)."),
				})),
			}
		},
		read: func(ctx context.Context, b *base, cfg tfsdk.Config, diags *diag.Diagnostics) any {
			var st listState
			var net types.Int64
			diags.Append(cfg.GetAttribute(ctx, path.Root("network_id"), &net)...)
			diags.Append(cfg.GetAttribute(ctx, path.Root("name"), &st.Name)...)
			diags.Append(cfg.GetAttribute(ctx, path.Root("status"), &st.Status)...)
			if diags.HasError() {
				return nil
			}
			st.NetworkID = net.ValueInt64()
			list, err := b.client.ListLoadBalancers(ctx, st.NetworkID, client.LBListFilter{Name: st.Name.ValueString(), Status: st.Status.ValueString()})
			if err != nil {
				reportListError(err, diags)
				return nil
			}
			st.CanManage = list.CanManage
			st.LoadBalancers = make([]summaryModel, 0, len(list.LoadBalancers))
			for _, l := range list.LoadBalancers {
				st.LoadBalancers = append(st.LoadBalancers, summaryModel{
					ID: l.ID, Name: l.Name, Description: l.Description, VIPAddress: l.VIPAddress, VIPSubnetID: l.VIPSubnetID,
					FloatingIP: l.FloatingIP, FloatingIPChecked: l.FloatingIPKnown, MemberCount: l.MembersCount, CreatedAt: l.CreatedAt,
					ProvisioningStatus: l.ProvisioningStatus, OperatingStatus: l.OperatingStatus,
				})
			}
			sortBy(st.LoadBalancers, func(m summaryModel) (string, string) { return m.Name, m.ID })
			return st
		},
	}}
}

func reportListError(err error, diags *diag.Diagnostics) {
	switch {
	case client.IsLBNotAvailable(err):
		diags.AddAttributeError(path.Root("network_id"), "Network not available", err.Error())
	default:
		diags.AddError("Unable to read load balancers", err.Error())
	}
}

// --- mtncloud_load_balancer

type lbState struct {
	NetworkID          int64           `tfsdk:"network_id"`
	ID                 string          `tfsdk:"id"`
	Name               string          `tfsdk:"name"`
	Description        string          `tfsdk:"description"`
	VIPAddress         string          `tfsdk:"vip_address"`
	VIPSubnetID        string          `tfsdk:"vip_subnet_id"`
	VIPSubnet          string          `tfsdk:"vip_subnet"`
	VIPPortID          string          `tfsdk:"vip_port_id"`
	CanManage          bool            `tfsdk:"can_manage"`
	CreatedAt          string          `tfsdk:"created_at"`
	UpdatedAt          string          `tfsdk:"updated_at"`
	Listeners          []listenerModel `tfsdk:"listeners"`
	Pools              []poolModel     `tfsdk:"pools"`
	Enabled            bool            `tfsdk:"enabled"`
	ProvisioningStatus string          `tfsdk:"provisioning_status"`
	OperatingStatus    string          `tfsdk:"operating_status"`
}

func newLoadBalancer() datasource.DataSource {
	return &dataSource{spec: spec{
		typeName: "load_balancer",
		about: "Looks up one load balancer on an MTN Network by ID or by name, including one made in the console, with its listeners, " +
			"and its pools with their members and health monitors. A name must match exactly one load balancer on the network. Each " +
			"load balancer is read once per run, however many data sources ask about it.",
		attributes: func() map[string]schema.Attribute {
			return merge(statusAttributes(), pickAttributes("", "load balancer"), map[string]schema.Attribute{
				"network_id":    networkIDAttribute(),
				"description":   str("Description, or empty."),
				"vip_address":   str("Private address it receives traffic on."),
				"vip_subnet_id": str("Subnet of that address."),
				"vip_subnet":    str("That subnet's name and CIDR, for example `app (10.0.0.0/24)`."),
				"vip_port_id":   str("Port that holds the address; a public IP attaches to it."),
				"can_manage":    flag("Whether your tenant owns the network, so it may change this load balancer."),
				"created_at":    str("When it was created (RFC 3339)."),
				"updated_at":    str("When it last changed (RFC 3339), or empty."),
				"listeners":     nested("Its listeners, sorted by name.", listenerAttributes()),
				"pools":         nested("Its pools, sorted by name, with their members and health monitors.", poolAttributes()),
			})
		},
		validators: func() []datasource.ConfigValidator {
			return []datasource.ConfigValidator{datasourcevalidator.ExactlyOneOf(path.MatchRoot("id"), path.MatchRoot("name"))}
		},
		read: func(ctx context.Context, b *base, cfg tfsdk.Config, diags *diag.Diagnostics) any {
			s := readSelector(ctx, cfg, "id", "name", diags)
			if diags.HasError() {
				return nil
			}
			lb, canManage := b.readLB(ctx, s.networkID, s.id, s.name, "id", "name", diags)
			if lb == nil {
				return nil
			}
			return lbState{
				NetworkID: s.networkID, ID: lb.ID, Name: lb.Name, Description: lb.Description, VIPAddress: lb.VIPAddress,
				VIPSubnetID: lb.VIPSubnetID, VIPSubnet: lb.VIPSubnet, VIPPortID: lb.VIPPortID, CanManage: canManage,
				CreatedAt: lb.CreatedAt, UpdatedAt: lb.UpdatedAt, Listeners: flattenListeners(lb.Listeners), Pools: flattenPools(lb.Pools),
				Enabled: lb.AdminStateUp, ProvisioningStatus: lb.ProvisioningStatus, OperatingStatus: lb.OperatingStatus,
			}
		},
	}}
}

// --- listeners

type listenersState struct {
	partOf
	Listeners []listenerModel `tfsdk:"listeners"`
}

func newListeners() datasource.DataSource {
	return &dataSource{spec: spec{
		typeName: "load_balancer_listeners",
		about:    "Lists a load balancer's listeners (the ports it receives traffic on), sorted by name. Pick the load balancer by ID or name.",
		attributes: func() map[string]schema.Attribute {
			return merge(lbSelectorAttributes(), map[string]schema.Attribute{"listeners": nested("The listeners, sorted by name.", listenerAttributes())})
		},
		validators: lbSelectorValidators,
		read: func(ctx context.Context, b *base, cfg tfsdk.Config, diags *diag.Diagnostics) any {
			lb, net := readPart(ctx, b, cfg, diags)
			if lb == nil {
				return nil
			}
			return listenersState{partOfLB(net, lb), flattenListeners(lb.Listeners)}
		},
	}}
}

type listenerState struct {
	partOf
	listenerModel
}

func newListener() datasource.DataSource {
	return &dataSource{spec: spec{
		typeName: "load_balancer_listener",
		about:    "Looks up one listener of a load balancer by ID or by name. Pick the load balancer by ID or name.",
		attributes: func() map[string]schema.Attribute {
			return merge(lbSelectorAttributes(), listenerAttributes(), pickAttributes("", "listener"))
		},
		validators: pickValidators(""),
		read: func(ctx context.Context, b *base, cfg tfsdk.Config, diags *diag.Diagnostics) any {
			id, name := pickInput(ctx, cfg, "", diags)
			lb, net := readPart(ctx, b, cfg, diags)
			if lb == nil {
				return nil
			}
			l, ok := pick(lb.Listeners, func(l client.LBListener) string { return l.ID }, func(l client.LBListener) string { return l.Name }, id, name, "listener", "", diags)
			if !ok {
				return nil
			}
			return listenerState{partOfLB(net, lb), flattenListener(l)}
		},
	}}
}

// --- pools

type poolsState struct {
	partOf
	Pools []poolModel `tfsdk:"pools"`
}

func newPools() datasource.DataSource {
	return &dataSource{spec: spec{
		typeName: "load_balancer_pools",
		about:    "Lists a load balancer's pools, sorted by name, each with its members and health monitor. Pick the load balancer by ID or name.",
		attributes: func() map[string]schema.Attribute {
			return merge(lbSelectorAttributes(), map[string]schema.Attribute{"pools": nested("The pools, sorted by name.", poolAttributes())})
		},
		validators: lbSelectorValidators,
		read: func(ctx context.Context, b *base, cfg tfsdk.Config, diags *diag.Diagnostics) any {
			lb, net := readPart(ctx, b, cfg, diags)
			if lb == nil {
				return nil
			}
			return poolsState{partOfLB(net, lb), flattenPools(lb.Pools)}
		},
	}}
}

type poolState struct {
	partOf
	poolModel
}

func pickPool(lb *client.LoadBalancer, id, name types.String, attr string, diags *diag.Diagnostics) (client.LBPool, bool) {
	return pick(lb.Pools, func(p client.LBPool) string { return p.ID }, func(p client.LBPool) string { return p.Name }, id, name, "pool", attr, diags)
}

func newPool() datasource.DataSource {
	return &dataSource{spec: spec{
		typeName: "load_balancer_pool",
		about:    "Looks up one pool of a load balancer by ID or by name, with its members and health monitor. Pick the load balancer by ID or name.",
		attributes: func() map[string]schema.Attribute {
			return merge(lbSelectorAttributes(), poolAttributes(), pickAttributes("", "pool"))
		},
		validators: pickValidators(""),
		read: func(ctx context.Context, b *base, cfg tfsdk.Config, diags *diag.Diagnostics) any {
			id, name := pickInput(ctx, cfg, "", diags)
			lb, net := readPart(ctx, b, cfg, diags)
			if lb == nil {
				return nil
			}
			p, ok := pickPool(lb, id, name, "", diags)
			if !ok {
				return nil
			}
			return poolState{partOfLB(net, lb), flattenPool(p)}
		},
	}}
}

// --- members

type membersState struct {
	partOf
	PoolID   string        `tfsdk:"pool_id"`
	PoolName string        `tfsdk:"pool_name"`
	Members  []memberModel `tfsdk:"members"`
}

func newMembers() datasource.DataSource {
	return &dataSource{spec: spec{
		typeName: "load_balancer_members",
		about:    "Lists the members (backends) of one pool of a load balancer, sorted by address. Pick the load balancer and the pool by ID or name.",
		attributes: func() map[string]schema.Attribute {
			return merge(lbSelectorAttributes(), pickAttributes("pool", "pool"), map[string]schema.Attribute{
				"members": nested("The pool's members, sorted by address.", memberAttributes()),
			})
		},
		validators: pickValidators("pool"),
		read: func(ctx context.Context, b *base, cfg tfsdk.Config, diags *diag.Diagnostics) any {
			id, name := pickInput(ctx, cfg, "pool", diags)
			lb, net := readPart(ctx, b, cfg, diags)
			if lb == nil {
				return nil
			}
			p, ok := pickPool(lb, id, name, "pool", diags)
			if !ok {
				return nil
			}
			return membersState{partOfLB(net, lb), p.ID, p.Name, flattenMembers(p.Members)}
		},
	}}
}

// --- health monitors

type monitorsState struct {
	partOf
	HealthMonitors []monitorModel `tfsdk:"health_monitors"`
}

func newHealthMonitors() datasource.DataSource {
	return &dataSource{spec: spec{
		typeName: "load_balancer_health_monitors",
		about:    "Lists a load balancer's health monitors (one per pool at most), sorted by pool name. Pick the load balancer by ID or name.",
		attributes: func() map[string]schema.Attribute {
			return merge(lbSelectorAttributes(), map[string]schema.Attribute{"health_monitors": nested("The health monitors, sorted by pool name.", monitorAttributes())})
		},
		validators: lbSelectorValidators,
		read: func(ctx context.Context, b *base, cfg tfsdk.Config, diags *diag.Diagnostics) any {
			lb, net := readPart(ctx, b, cfg, diags)
			if lb == nil {
				return nil
			}
			return monitorsState{partOfLB(net, lb), flattenMonitors(lb.Pools)}
		},
	}}
}

type monitorState struct {
	partOf
	monitorModel
}

func newHealthMonitor() datasource.DataSource {
	return &dataSource{spec: spec{
		typeName: "load_balancer_health_monitor",
		about: "Looks up one health monitor of a load balancer by ID or by name. Pick the load balancer by ID or name. A monitor's name " +
			"is often empty; look it up by ID then, or read it from its pool.",
		attributes: func() map[string]schema.Attribute {
			return merge(lbSelectorAttributes(), monitorAttributes(), pickAttributes("", "health monitor"))
		},
		validators: pickValidators(""),
		read: func(ctx context.Context, b *base, cfg tfsdk.Config, diags *diag.Diagnostics) any {
			id, name := pickInput(ctx, cfg, "", diags)
			lb, net := readPart(ctx, b, cfg, diags)
			if lb == nil {
				return nil
			}
			m, ok := pick(flattenMonitors(lb.Pools), func(m monitorModel) string { return m.ID }, func(m monitorModel) string { return m.Name }, id, name, "health monitor", "", diags)
			if !ok {
				return nil
			}
			return monitorState{partOfLB(net, lb), m}
		},
	}}
}

// --- floating IP pools

type fipPoolModel struct {
	Name string `tfsdk:"name"`
	ID   string `tfsdk:"id"`
}

type fipAddressModel struct {
	ID      string `tfsdk:"id"`
	Address string `tfsdk:"address"`
}

type fipState struct {
	NetworkID int64             `tfsdk:"network_id"`
	Pools     []fipPoolModel    `tfsdk:"pools"`
	Available []fipAddressModel `tfsdk:"available_addresses"`
}

func newFloatingIPPools() datasource.DataSource {
	return &dataSource{spec: spec{
		typeName: "load_balancer_floating_ip_pools",
		about: "Lists where a load balancer on an MTN Network can get a public IP: the floating IP pools, and the addresses your " +
			"project already holds that aren't attached to anything.",
		attributes: func() map[string]schema.Attribute {
			return map[string]schema.Attribute{
				"network_id": networkIDAttribute(),
				"pools": nested("Floating IP pools, sorted by name.", map[string]schema.Attribute{
					"name": str("Pool name, for example `public-network-01`."),
					"id":   str("Pool ID."),
				}),
				"available_addresses": nested("Addresses the project holds that are free to attach, sorted by address.", map[string]schema.Attribute{
					"id":      str("Floating IP ID."),
					"address": str("The public IP."),
				}),
			}
		},
		read: func(ctx context.Context, b *base, cfg tfsdk.Config, diags *diag.Diagnostics) any {
			var net types.Int64
			diags.Append(cfg.GetAttribute(ctx, path.Root("network_id"), &net)...)
			if diags.HasError() {
				return nil
			}
			o, err := b.client.LBFloatingIPChoices(ctx, net.ValueInt64())
			if err != nil {
				reportListError(err, diags)
				return nil
			}
			st := fipState{NetworkID: net.ValueInt64(), Pools: []fipPoolModel{}, Available: []fipAddressModel{}}
			for _, p := range o.Pools {
				st.Pools = append(st.Pools, fipPoolModel{Name: p.Name, ID: p.ExternalID})
			}
			for _, a := range o.Available {
				st.Available = append(st.Available, fipAddressModel{ID: a.ID, Address: a.Address})
			}
			sortBy(st.Pools, func(m fipPoolModel) (string, string) { return m.Name, m.ID })
			sortBy(st.Available, func(m fipAddressModel) (string, string) { return m.Address, m.ID })
			return st
		},
	}}
}
