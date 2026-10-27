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

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/knownvalue"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/statecheck"
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
}

func newNetStore() *netStore {
	return &netStore{nextID: 9300, networks: map[int64]map[string]any{}, inUse: map[int64]bool{},
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
	rest := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/networks"), "/")
	if rest == "" {
		switch r.Method {
		case http.MethodGet:
			list := []any{}
			for _, n := range s.networks {
				if name := r.URL.Query().Get("name"); name == "" || n["name"] == name {
					list = append(list, n)
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
			if s.lostReplies > 0 {
				s.lostReplies--
				reply(http.StatusBadGateway, map[string]any{"success": false, "msg": "Bad Gateway"})
				return
			}
			reply(http.StatusOK, map[string]any{"success": true, "network": n})
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
		reply(http.StatusOK, map[string]any{"network": n})
	case http.MethodPut:
		for k, v := range body["network"].(map[string]any) {
			n[k] = v
		}
		reply(http.StatusOK, map[string]any{"success": true, "network": n})
	case http.MethodDelete:
		if s.inUse[id] {
			reply(http.StatusBadRequest, map[string]any{"success": false, "msg": "Unable to complete operation on network " +
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
					netCheck("project", knownvalue.StringExact("web-prod-Acme")),
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
				PreConfig: lock(func() { store.status = "" }),
				Config:    netConfig(f, moved),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction("mtncloud_network.app", plancheck.ResourceActionDestroyBeforeCreate),
				}},
			},
		},
	})
	if n := len(store.networks); n != 0 {
		t.Errorf("%d networks left after destroy", n)
	}
}
