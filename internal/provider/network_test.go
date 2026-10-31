package provider

import (
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/knownvalue"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/statecheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"
)

// netStore is an in-memory MTN Network API shaped like the captures in dev-docs/api-research.md:
// a network comes back with its project, one subnet, and zone instead of networkServer.
type netStore struct {
	mu       sync.Mutex
	nextID   int64
	networks map[int64]map[string]any
	projects map[int64]string
	inUse    map[int64]bool // DELETE answers "ports still in use"
	creates  int
	refuse   string // when set, the next create answers 200 {"success": false, "msg": refuse}
	// lostReplies makes the next N creates succeed but answer 502; status gives new networks
	// another status than "ok"; upperIPv6 echoes IPv6 values in upper case.
	lostReplies int
	status      string
	upperIPv6   bool
	// A new network answers "provisioning" in the create reply and on its first read, as the
	// platform does (captured 2026-10-27); stuck keeps every network there.
	pending   map[int64]bool
	stuck     bool
	slowReply time.Duration // creates are applied at once but answered this late
	// refuseRoutes fails a network (status "failed", after "success") whose host routes name it,
	// as the network service does with a value it refuses; puts records each PUT's config.
	refuseRoutes string
	puts         []any
	failLater    map[int64]bool // created "ok" with refused routes: failed after its first GET
}

func newNetStore() *netStore {
	return &netStore{nextID: 9300, networks: map[int64]map[string]any{}, inUse: map[int64]bool{}, pending: map[int64]bool{},
		projects: map[int64]string{9201: "web-prod-Acme", 9202: "data-Acme"}}
}

func (s *netStore) byName(name string) int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, n := range s.networks {
		if n["name"] == name {
			return id
		}
	}
	return 0
}

func (s *netStore) set(id int64, k string, v any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.networks[id][k] = v
}

func (s *netStore) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	reply := func(status int, v any) {
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(v)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	view := func(id int64) map[string]any {
		out := map[string]any{}
		for k, v := range s.networks[id] {
			out[k] = v
		}
		if s.pending[id] || s.stuck {
			out["status"] = "provisioning"
			delete(s.pending, id)
		}
		return out
	}
	rest := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/networks"), "/")
	if rest == "" {
		switch r.Method {
		case http.MethodGet:
			list := []any{}
			for id, n := range s.networks {
				if name := r.URL.Query().Get("name"); name == "" || n["name"] == name {
					list = append(list, view(id))
				}
			}
			reply(http.StatusOK, map[string]any{"networks": list, "meta": map[string]any{"total": len(list)}})
		case http.MethodPost:
			s.creates++
			if s.refuse != "" {
				msg := s.refuse
				s.refuse = ""
				reply(http.StatusOK, map[string]any{"success": false, "msg": msg})
				return
			}
			in := body["network"].(map[string]any)
			pool := int64(in["zonePool"].(map[string]any)["id"].(float64))
			s.nextID++
			id := s.nextID
			n := map[string]any{"id": id, "zone": map[string]any{"id": 4, "name": "MTNNG_CLOUD_AZ_1"},
				"externalId": fmt.Sprintf("00000000-0000-0000-0000-%012d", id), "status": "ok",
				"zonePool": map[string]any{"id": pool, "name": s.projects[pool]},
				"subnets":  []any{map[string]any{"id": id + 10, "name": fmt.Sprint(in["name"], " subnet"), "cidr": in["cidr"]}},
				"config":   map[string]any{"dhcpRange": "", "hostRoutes": ""}}
			for _, k := range []string{"name", "description", "cidr", "gateway", "dnsPrimary", "dnsSecondary", "dhcpServer",
				"allowStaticOverride", "ipv6Enabled", "cidrIPv6", "gatewayIPv6", "dnsPrimaryIPv6", "dnsSecondaryIPv6", "dhcpServerIPv6"} {
				n[k] = in[k]
			}
			if c, ok := in["config"]; ok {
				n["config"] = c
				if s.refuseRoutes != "" && strings.Contains(fmt.Sprint(c.(map[string]any)["hostRoutes"]), s.refuseRoutes) {
					s.failLater = map[int64]bool{id: true}
				}
			}
			if s.status != "" {
				n["status"] = s.status
			}
			if s.upperIPv6 {
				for _, k := range []string{"cidrIPv6", "gatewayIPv6"} {
					if v, ok := n[k].(string); ok {
						n[k] = strings.ToUpper(v)
					}
				}
			}
			s.networks[id] = n
			s.pending[id] = true
			created := view(id)
			s.pending[id] = true // the first read is still "provisioning" too
			if d := s.slowReply; d > 0 {
				s.mu.Unlock() // reads go on while this reply is late
				select {
				case <-time.After(d):
				case <-r.Context().Done():
				}
				s.mu.Lock()
			}
			if s.lostReplies > 0 {
				s.lostReplies--
				reply(http.StatusBadGateway, map[string]any{"success": false, "msg": "Bad Gateway"})
				return
			}
			reply(http.StatusOK, map[string]any{"success": true, "network": created})
		}
		return
	}
	id, _ := strconv.ParseInt(rest, 10, 64)
	n := s.networks[id]
	if n == nil {
		reply(http.StatusNotFound, map[string]any{"success": false, "msg": "Network not found"})
		return
	}
	switch r.Method {
	case http.MethodGet:
		v := view(id)
		reply(http.StatusOK, map[string]any{"network": v})
		if s.failLater[id] && v["status"] == "ok" { // read "ok" once, then failed
			delete(s.failLater, id)
			n["status"] = "failed"
		}
	case http.MethodPut:
		for k, v := range body["network"].(map[string]any) {
			n[k] = v
		}
		s.puts = append(s.puts, body["network"].(map[string]any)["config"])
		if c, _ := n["config"].(map[string]any); s.refuseRoutes != "" && c != nil && strings.Contains(fmt.Sprint(c["hostRoutes"]), s.refuseRoutes) {
			n["status"] = "failed"
		} else if n["status"] == "failed" {
			n["status"] = "ok" // the network service took the settings sent again
		}
		reply(http.StatusOK, map[string]any{"success": true, "network": n})
	case http.MethodDelete:
		if s.inUse[id] {
			// The platform answers 200 with "success": false (captured 2026-10-27).
			reply(http.StatusOK, map[string]any{"success": false, "msg": "Unable to complete operation on network " +
				n["externalId"].(string) + ". There are one or more ports still in use on the network, id for these ports is: 00000000-0000-0000-0000-0000000000p1."})
			return
		}
		delete(s.networks, id)
		reply(http.StatusOK, map[string]any{"success": true})
	}
}

// netConfig is one network. cidr and gateway default to 10.20.1.0/24 and .1 unless attrs sets them.
func netConfig(f *fakeAPI, attrs string) string {
	for k, v := range map[string]string{"cidr": "10.20.1.0/24", "gateway": "10.20.1.1"} {
		if !regexp.MustCompile(`(?m)^\s*` + k + `\s*=`).MatchString(attrs) {
			attrs += "\n  " + k + " = \"" + v + "\""
		}
	}
	return f.providerConfig() + `
resource "mtncloud_network" "app" {
  name = "app-net"
` + attrs + `
}
`
}

func netCheck(attr string, v knownvalue.Check) statecheck.StateCheck {
	return statecheck.ExpectKnownValue("mtncloud_network.app", tfjsonpath.New(attr), v)
}

func TestNetwork_Lifecycle(t *testing.T) {
	f := newFakeAPI(t)
	store := newNetStore()
	f.net = store
	app := func() int64 { return store.byName("app-net") }

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories(),
		Steps: []resource.TestStep{
			{
				Config: netConfig(f, `  project = "web-prod"`),
				ConfigStateChecks: []statecheck.StateCheck{
					netCheck("project", knownvalue.StringExact("web-prod")),
					netCheck("dns_primary", knownvalue.StringExact("102.88.134.41")),
					netCheck("dns_secondary", knownvalue.StringExact("102.90.109.125")),
					netCheck("dhcp_enabled", knownvalue.Bool(true)),
					netCheck("allow_ip_override", knownvalue.Bool(true)),
					netCheck("ipv6", knownvalue.Null()),
					netCheck("description", knownvalue.Null()),
					netCheck("subnet_id", knownvalue.NotNull()),
				},
			},
			{ResourceName: "mtncloud_network.app", ImportState: true, ImportStateVerify: true},
			{
				// In place: description, gateway, DNS, DHCP; the project by its full name is the same project.
				Config: netConfig(f, `  project = "web-prod-Acme"
  description = "app tier"
  gateway = "10.20.1.254"
  dns_primary = "102.89.90.112"
  dhcp_enabled = false`),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction("mtncloud_network.app", plancheck.ResourceActionUpdate),
				}},
				ConfigStateChecks: []statecheck.StateCheck{
					netCheck("gateway", knownvalue.StringExact("10.20.1.254")),
					// The same project by another name: the state's spelling stays.
					netCheck("project", knownvalue.StringExact("web-prod")),
				},
			},
			{
				// Changed outside Terraform: the plan puts it back.
				PreConfig: func() { store.set(app(), "dnsSecondary", "8.8.4.4") },
				Config: netConfig(f, `  project = "web-prod-Acme"
  description = "app tier"
  gateway = "10.20.1.254"
  dns_primary = "102.89.90.112"
  dhcp_enabled = false`),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction("mtncloud_network.app", plancheck.ResourceActionUpdate),
				}},
				ConfigStateChecks: []statecheck.StateCheck{netCheck("dns_secondary", knownvalue.StringExact("102.90.109.125"))},
			},
			{
				// Turning IPv6 on replaces the network.
				Config: netConfig(f, `  project = "web-prod"
  ipv6 = { cidr = "2001:db8:1::/64", gateway = "2001:db8:1::1" }`),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction("mtncloud_network.app", plancheck.ResourceActionDestroyBeforeCreate),
				}},
				ConfigStateChecks: []statecheck.StateCheck{
					netCheck("ipv6", knownvalue.ObjectPartial(map[string]knownvalue.Check{
						"cidr": knownvalue.StringExact("2001:db8:1::/64"), "dhcp_enabled": knownvalue.Bool(false),
					})),
				},
			},
			{
				// A new IPv6 DNS server is an update; a new IPv4 range is a replacement.
				Config: netConfig(f, `  project = "web-prod"
  ipv6 = { cidr = "2001:db8:1::/64", gateway = "2001:db8:1::1", dns_primary = "2001:db8::53" }`),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction("mtncloud_network.app", plancheck.ResourceActionUpdate),
				}},
			},
			{
				// Only the IPv6 range changes: a replacement too.
				Config: netConfig(f, `  project = "web-prod"
  ipv6 = { cidr = "2001:db8:2::/64", gateway = "2001:db8:2::1", dns_primary = "2001:db8::53" }`),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction("mtncloud_network.app", plancheck.ResourceActionDestroyBeforeCreate),
				}},
			},
			{
				Config: strings.Replace(netConfig(f, `  project = "web-prod"
  ipv6 = { cidr = "2001:db8:1::/64", gateway = "2001:db8:1::1", dns_primary = "2001:db8::53" }`), "10.20.1.", "10.20.2.", 2),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction("mtncloud_network.app", plancheck.ResourceActionDestroyBeforeCreate),
				}},
			},
			{
				// Deleted outside Terraform: planned again.
				PreConfig: func() { id := app(); store.mu.Lock(); delete(store.networks, id); store.mu.Unlock() },
				Config:    netConfig(f, `  project = "data"`),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction("mtncloud_network.app", plancheck.ResourceActionCreate),
				}},
			},
			{
				// Another project replaces it.
				Config: netConfig(f, `  project = "web-prod"`),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction("mtncloud_network.app", plancheck.ResourceActionDestroyBeforeCreate),
				}},
			},
			{
				// Still used by instances: destroy fails with a hint. Then free it so cleanup works.
				PreConfig:   func() { store.mu.Lock(); store.inUse[store.nextID] = true; store.mu.Unlock() },
				Config:      f.providerConfig(),
				ExpectError: regexp.MustCompile(`still attached to this network`),
			},
			{
				PreConfig: func() { store.mu.Lock(); clear(store.inUse); store.mu.Unlock() },
				Config:    f.providerConfig(),
			},
		},
	})
	if n := len(store.networks); n != 0 {
		t.Errorf("%d networks left after destroy", n)
	}
}

// The provider's project fills in a network that names none; state keeps it, so a new default
// replaces the network, and with no project anywhere the plan fails.
func TestNetwork_ProviderProject(t *testing.T) {
	f := newFakeAPI(t)
	store := newNetStore()
	f.net = store
	withProject := func(project, attrs string) string {
		cfg := netConfig(f, attrs)
		if project == "" {
			return cfg
		}
		return strings.Replace(cfg, `access_token = "test-token"`, `access_token = "test-token"
  project      = "`+project+`"`, 1)
	}
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories(),
		Steps: []resource.TestStep{
			{Config: withProject("", ""), ExpectError: regexp.MustCompile(`(?s)No project set.*MTNCLOUD_PROJECT`)},
			{
				Config:            withProject("web-prod", ""),
				ConfigStateChecks: []statecheck.StateCheck{netCheck("project", knownvalue.StringExact("web-prod"))},
			},
			{
				// The resource's own project wins over the provider's; the same project by its full name is
				// no change.
				Config:            withProject("data", `  project = "web-prod-Acme"`),
				ConfigPlanChecks:  resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()}},
				ConfigStateChecks: []statecheck.StateCheck{netCheck("project", knownvalue.StringExact("web-prod"))},
			},
			{
				// A provider default spelled in full, against state's short name: no change either.
				Config:           withProject("web-prod-Acme", ""),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()}},
			},
			{
				// An import block relying on the default plans a no-op import, with no update.
				Config:          withProject("web-prod-Acme", ""),
				ResourceName:    "mtncloud_network.app",
				ImportState:     true,
				ImportStateKind: resource.ImportBlockWithID,
			},
			{
				Config: withProject("data", ""),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction("mtncloud_network.app", plancheck.ResourceActionReplace),
				}},
				ConfigStateChecks: []statecheck.StateCheck{netCheck("project", knownvalue.StringExact("data"))},
			},
			{
				PreConfig:        func() { t.Setenv("MTNCLOUD_PROJECT", "data") },
				Config:           withProject("", ""),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()}},
			},
		},
	})
}

func TestNetwork_Refusals(t *testing.T) {
	f := newFakeAPI(t)
	store := newNetStore()
	f.net = store
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories(),
		Steps: []resource.TestStep{
			{
				// The platform refuses a bad design with 200 {"success": false}: an error, nothing in state.
				PreConfig: func() {
					store.mu.Lock()
					store.refuse = "Network CIDR overlaps with an existing network"
					store.mu.Unlock()
				},
				Config:      netConfig(f, `  project = "web-prod"`),
				ExpectError: regexp.MustCompile(`request refused: Network CIDR overlaps`),
			},
			{
				// A network already called that in the project is never adopted.
				PreConfig: func() {
					store.mu.Lock()
					store.networks[9999] = map[string]any{"id": 9999, "name": "app-net", "zonePool": map[string]any{"id": 9201, "name": "web-prod-Acme"}}
					store.mu.Unlock()
				},
				Config:      netConfig(f, `  project = "web-prod"`),
				ExpectError: regexp.MustCompile(`Network already exists`),
			},
			{
				PreConfig: func() { store.mu.Lock(); delete(store.networks, 9999); store.mu.Unlock() },
				Config:    f.providerConfig(),
			},
		},
	})
	if store.creates != 1 {
		t.Errorf("%d create calls, want 1 (the refused one)", store.creates)
	}
}

func TestNetwork_PlanTimeErrors(t *testing.T) {
	f := newFakeAPI(t)
	f.net = newNetStore()
	for name, tc := range map[string]struct{ attrs, want string }{
		"gateway outside":        {`project = "web-prod"` + "\n" + `gateway = "10.9.9.1"`, `Gateway outside the network`},
		"gateway is broadcast":   {`project = "web-prod"` + "\n" + `gateway = "10.20.1.255"`, `Gateway is the broadcast address`},
		"gateway is network":     {`project = "web-prod"` + "\n" + `gateway = "10.20.1.0"`, `Gateway is the network address`},
		"ipv6 cidr as cidr":      {`project = "web-prod"` + "\n" + `cidr = "2001:db8::/64"`, `Wrong address family`},
		"host bits":              {`project = "web-prod"` + "\n" + `cidr = "10.20.1.5/24"`, `Use "10.20.1.0/24"`},
		"bad dns":                {`project = "web-prod"` + "\n" + `dns_primary = "dns.example"`, `must be an IPv4 address`},
		"ipv6 gateway outside":   {`project = "web-prod"` + "\n" + `ipv6 = { cidr = "2001:db8:1::/64", gateway = "2001:db8:2::1" }`, `Gateway outside the network`},
		"unknown project":        {`project = "web-prd"`, `Unknown project`},
		"ipv6 without a gateway": {`project = "web-prod"` + "\n" + `ipv6 = { cidr = "2001:db8:1::/64" }`, `gateway`},
	} {
		t.Run(name, func(t *testing.T) {
			cfg := netConfig(f, tc.attrs)
			resource.UnitTest(t, resource.TestCase{
				ProtoV6ProviderFactories: factories(),
				Steps:                    []resource.TestStep{{Config: cfg, PlanOnly: true, ExpectError: regexp.MustCompile(regexp.QuoteMeta(tc.want))}},
			})
		})
	}
}

func TestNetwork_FailuresAndIPv6(t *testing.T) {
	f := newFakeAPI(t)
	store := newNetStore()
	f.net = store
	lock := func(fn func()) func() { return func() { store.mu.Lock(); fn(); store.mu.Unlock() } }
	dual := `  project = "web-prod"
  ipv6 = { cidr = "2001:db8:3::/64", gateway = "2001:db8:3::1" }`
	moved := `  project = "web-prod"
  cidr = "10.20.9.0/24"
  gateway = "10.20.9.1"`
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories(),
		Steps: []resource.TestStep{
			{
				// The reply is lost and the API echoes IPv6 in upper case: one network, and no diff.
				PreConfig: lock(func() { store.lostReplies, store.upperIPv6 = 1, true }),
				Config:    netConfig(f, dual),
				ConfigStateChecks: []statecheck.StateCheck{
					netCheck("ipv6", knownvalue.ObjectPartial(map[string]knownvalue.Check{"gateway": knownvalue.StringExact("2001:db8:3::1")})),
				},
			},
			{ResourceName: "mtncloud_network.app", ImportState: true, ImportStateVerify: true},
			{
				// IPv6 off replaces the network.
				PreConfig: lock(func() {
					if n := len(store.networks); n != 1 {
						t.Errorf("%d networks after a lost reply, want 1", n)
					}
				}),
				Config: netConfig(f, `  project = "web-prod"`),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction("mtncloud_network.app", plancheck.ResourceActionDestroyBeforeCreate),
				}},
				ConfigStateChecks: []statecheck.StateCheck{netCheck("ipv6", knownvalue.Null())},
			},
			{
				// Created, but failed: reported, and kept in state for the next apply to replace.
				PreConfig:   lock(func() { store.status = "failed" }),
				Config:      netConfig(f, moved),
				ExpectError: regexp.MustCompile(`Network creation failed`),
			},
			{
				// Stuck in "provisioning": the create timeout ends the wait, and it is kept in state.
				PreConfig: lock(func() { store.status, store.stuck = "", true }),
				Config: netConfig(f, moved+`
  timeouts {
    create = "2s"
  }`),
				ExpectError: regexp.MustCompile(`(?s)Timed out waiting for the network.*still "provisioning"`),
			},
			{
				// The timeout ends the wait for the create's reply: the network is found and kept.
				PreConfig: lock(func() {
					store.stuck, store.slowReply = false, 5*time.Second
					for id, n := range store.networks {
						if n["cidr"] == "10.20.9.0/24" {
							delete(store.networks, id) // the stuck one: gone, so the next create starts clean
						}
					}
				}),
				Config: netConfig(f, moved+`
  timeouts {
    create = "1s"
  }`),
				ExpectError: regexp.MustCompile(`Timed out waiting for the network`),
			},
			{
				PreConfig: lock(func() {
					n := 0
					for _, x := range store.networks {
						if x["cidr"] == "10.20.9.0/24" {
							n++
						}
					}
					if n != 1 {
						t.Errorf("%d networks after a timed-out create, want 1 (kept in state, not orphaned)", n)
					}
					store.slowReply = 0
				}),
				Config: netConfig(f, moved),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction("mtncloud_network.app", plancheck.ResourceActionDestroyBeforeCreate),
				}},
			},
			{
				// Replaced and healthy: nothing left to do.
				Config:           netConfig(f, moved),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()}},
			},
		},
	})
	if n := len(store.networks); n != 0 {
		t.Errorf("%d networks left after destroy", n)
	}
}

// DHCP range and host routes: sent as the network type's config strings, read back without a
// diff, cleared with "" when removed, checked at plan, and a refusal after "success" is an error.
func TestNetwork_DHCPRangeAndRoutes(t *testing.T) {
	f := newFakeAPI(t)
	store := newNetStore()
	f.net = store
	both := `  dhcp_range  = { start = "10.20.1.50", end = "10.20.1.99" }
  host_routes = [
    { destination = "10.99.0.0/16", next_hop = "10.20.1.5" },
    { destination = "10.98.0.0/16", next_hop = "10.20.1.5" },
  ]`
	config := func() map[string]any {
		store.mu.Lock()
		defer store.mu.Unlock()
		c, _ := store.networks[store.nextID]["config"].(map[string]any)
		return c
	}
	refused := netConfig(f, `  project = "web-prod"
  host_routes = [{ destination = "10.97.0.0/16", next_hop = "10.20.1.5" }]`)
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories(),
		Steps: []resource.TestStep{
			{Config: netConfig(f, `  project = "web-prod"
  dhcp_range = { start = "10.20.2.5", end = "10.20.2.9" }`), PlanOnly: true,
				ExpectError: regexp.MustCompile(`DHCP range outside the network`)},
			{Config: netConfig(f, `  project = "web-prod"
  dhcp_range = { start = "10.20.1.99", end = "10.20.1.50" }`), PlanOnly: true,
				ExpectError: regexp.MustCompile(`DHCP range in the wrong order`)},
			{Config: netConfig(f, `  project = "web-prod"
  dhcp_range = { start = "10.20.1.1", end = "10.20.1.50" }`), PlanOnly: true,
				ExpectError: regexp.MustCompile(`DHCP range holds the gateway`)},
			{Config: netConfig(f, `  project = "web-prod"
  host_routes = []`), PlanOnly: true, ExpectError: regexp.MustCompile(`(?s)host_routes.*at least 1`)},
			{Config: netConfig(f, `  project = "web-prod"
`+both), Check: func(*terraform.State) error {
				c := config()
				if c["dhcpRange"] != "10.20.1.50,10.20.1.99" || c["hostRoutes"] != "10.98.0.0/16,10.20.1.5\n10.99.0.0/16,10.20.1.5" {
					t.Errorf("config sent %v", c)
				}
				return nil
			}},
			{Config: netConfig(f, `  project = "web-prod"
`+both), ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()}}},
			// Removing both clears them.
			{Config: netConfig(f, `  project = "web-prod"`), Check: func(*terraform.State) error {
				if c := config(); c["dhcpRange"] != "" || c["hostRoutes"] != "" {
					t.Errorf("config after removing both %v, want both cleared", c)
				}
				return nil
			}},
			{Config: netConfig(f, `  project = "web-prod"`), ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()}}},
			{PreConfig: func() { store.refuseRoutes = "10.97.0.0/16" }, Config: refused,
				ExpectError: regexp.MustCompile(`(?s)Network update refused.*now "failed"`)},
			// Left failed, the next plan sends the settings again with nothing changed in the
			// configuration (here the network service now takes them).
			{PreConfig: func() { store.refuseRoutes = "" }, Config: refused, ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
				plancheck.ExpectResourceAction("mtncloud_network.app", plancheck.ResourceActionUpdate)}},
				ConfigStateChecks: []statecheck.StateCheck{statecheck.ExpectKnownValue("mtncloud_network.app", tfjsonpath.New("status"), knownvalue.StringExact("ok"))}},
			{Config: refused, ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()}}},
		},
	})
}

// A network created with routes the network service refuses reads "ok", then "failed": the create
// watches it and fails, so the next apply replaces it.
func TestNetwork_CreateRefused(t *testing.T) {
	f := newFakeAPI(t)
	store := newNetStore()
	f.net = store
	store.refuseRoutes = "10.97.0.0/16"
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories(),
		Steps: []resource.TestStep{{Config: netConfig(f, `  project = "web-prod"
  host_routes = [{ destination = "10.97.0.0/16", next_hop = "10.20.1.5" }]`),
			ExpectError: regexp.MustCompile(`(?s)Network creation failed.*"failed"`)}},
	})
}
