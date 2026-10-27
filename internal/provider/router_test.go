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
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"
)

// routerStore is an in-memory MTN Router API shaped like the live captures in
// dev-docs/api-research.md: a router is "provisioning" on the first read after a write, the
// project isn't echoed, a rename is refused, and externalIp goes stale when the gateway is removed.
// Subnet IDs map to networks as netStore makes them (network ID + 10).
type routerStore struct {
	mu          sync.Mutex
	nextID      int64
	routers     map[int64]map[string]any
	pending     map[int64]bool // the next read answers "provisioning"
	ips         int            // gateway addresses handed out
	lostReplies int            // the next N creates succeed but answer 502
	fail        bool           // new routers settle as "failed"
	stuck       bool           // routers never leave "provisioning"
	slowReply   time.Duration  // creates are applied at once but answered this late
	creates     int
}

var extNetworks = map[int64]string{34: "public-network-01", 32: "public-network-02"} // options_external_networks.json

func newRouterStore() *routerStore {
	return &routerStore{nextID: 9600, routers: map[int64]map[string]any{}, pending: map[int64]bool{}}
}

func (s *routerStore) byName(name string) int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, r := range s.routers {
		if r["name"] == name {
			return id
		}
	}
	return 0
}

// apply writes the gateway and interfaces of a create or update body into a router.
func (s *routerStore) apply(r, in map[string]any) {
	for _, k := range []string{"description", "enabled"} {
		if v, ok := in[k]; ok {
			r[k] = v
		}
	}
	var gateway map[string]any
	if ext, ok := in["externalNetwork"]; ok {
		r["externalNetwork"] = nil
		if ext != nil {
			id := int64(ext.(map[string]any)["id"].(float64))
			r["externalNetwork"] = map[string]any{"id": id, "name": extNetworks[id]}
			s.ips++
			r["externalIp"] = fmt.Sprintf("203.0.113.%d", s.ips)
			gateway = map[string]any{"ipAddress": r["externalIp"], "network": r["externalNetwork"]}
		} // removed: externalIp keeps the old address, as the platform does
	} else if old := r["externalNetwork"]; old != nil {
		gateway = map[string]any{"ipAddress": r["externalIp"], "network": old}
	}
	var internal []any
	if ifs, ok := in["interfaces"]; ok {
		for _, sub := range ifs.(map[string]any)["networkSubnet"].(map[string]any)["id"].([]any) {
			internal = append(internal, map[string]any{"ipAddress": nil, "network": map[string]any{"id": int64(sub.(float64)) - 10}})
		}
	} else {
		for _, i := range r["interfaces"].([]any) {
			if ext, _ := r["externalNetwork"].(map[string]any); ext == nil || i.(map[string]any)["network"].(map[string]any)["id"] != ext["id"] {
				internal = append(internal, i)
			}
		}
	}
	ifaces := []any{}
	if gateway != nil {
		ifaces = append(ifaces, gateway)
	}
	r["interfaces"] = append(ifaces, internal...)
}

func (s *routerStore) ServeHTTP(w http.ResponseWriter, r *http.Request) {
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
		for k, v := range s.routers[id] {
			out[k] = v
		}
		if s.pending[id] || s.stuck {
			out["status"], out["externalId"] = "provisioning", nil
			delete(s.pending, id)
		}
		return out
	}
	rest := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/networks/routers"), "/")
	if rest == "" {
		switch r.Method {
		case http.MethodGet:
			list := []any{}
			for id, rt := range s.routers {
				if name := r.URL.Query().Get("name"); name == "" || rt["name"] == name {
					list = append(list, view(id))
				}
			}
			reply(http.StatusOK, map[string]any{"networkRouters": list, "meta": map[string]any{"total": len(list)}})
		case http.MethodPost:
			s.creates++
			in := body["networkRouter"].(map[string]any)
			s.nextID++
			id := s.nextID
			rt := map[string]any{"id": id, "name": in["name"], "status": "ok", "poolId": nil, "interfaces": []any{},
				"externalId": fmt.Sprintf("00000000-0000-0000-0000-%012d", id)}
			if s.fail {
				rt["status"] = "failed"
			}
			s.apply(rt, in)
			s.routers[id], s.pending[id] = rt, true
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
				reply(http.StatusBadGateway, map[string]any{"success": false})
				return
			}
			reply(http.StatusOK, map[string]any{"success": true, "id": id})
		}
		return
	}
	id, _ := strconv.ParseInt(rest, 10, 64)
	if s.routers[id] == nil {
		reply(http.StatusNotFound, map[string]any{"success": false, "msg": "Network Router not found"})
		return
	}
	switch r.Method {
	case http.MethodGet:
		reply(http.StatusOK, map[string]any{"networkRouter": view(id)})
	case http.MethodPut:
		in := body["networkRouter"].(map[string]any)
		if _, ok := in["name"]; ok {
			reply(http.StatusBadRequest, map[string]any{"success": false, "errors": map[string]any{"name": "Field name cannot be changed"}})
			return
		}
		s.apply(s.routers[id], in)
		s.pending[id] = true
		reply(http.StatusOK, map[string]any{"success": true})
	case http.MethodDelete:
		delete(s.routers, id)
		reply(http.StatusOK, map[string]any{"success": true})
	}
}

// routerConfig is two networks in web-prod, one in data, and a router "edge" with attrs.
func routerConfig(f *fakeAPI, attrs string) string {
	return f.providerConfig() + `
resource "mtncloud_network" "app" {
  name    = "app-net"
  project = "web-prod"
  cidr    = "10.20.1.0/24"
  gateway = "10.20.1.1"
}

resource "mtncloud_network" "db" {
  name    = "db-net"
  project = "web-prod"
  cidr    = "10.20.2.0/24"
  gateway = "10.20.2.1"
}

resource "mtncloud_network" "other" {
  name    = "other-net"
  project = "data"
  cidr    = "10.30.1.0/24"
  gateway = "10.30.1.1"
}

resource "mtncloud_router" "edge" {
  project = "web-prod"
` + attrs + `
}
`
}

func routerCheck(attr string, v knownvalue.Check) statecheck.StateCheck {
	return statecheck.ExpectKnownValue("mtncloud_router.edge", tfjsonpath.New(attr), v)
}

func routerPlan(action plancheck.ResourceActionType, more ...plancheck.PlanCheck) resource.ConfigPlanChecks {
	return resource.ConfigPlanChecks{PreApply: append([]plancheck.PlanCheck{plancheck.ExpectResourceAction("mtncloud_router.edge", action)}, more...)}
}

func newRouterFakes(t *testing.T) (*fakeAPI, *netStore, *routerStore) {
	f := newFakeAPI(t)
	nets, routers := newNetStore(), newRouterStore()
	f.net, f.router = nets, routers
	return f, nets, routers
}

func TestRouter_Lifecycle(t *testing.T) {
	f, nets, routers := newRouterFakes(t)
	edge := func() int64 { return routers.byName("edge") }
	appOnly := `  name = "edge"
  networks = [mtncloud_network.app.id]
  external_network = "public-network-02"`
	both := `  name = "edge"
  networks = [mtncloud_network.app.id, mtncloud_network.db.id]`

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories(),
		Steps: []resource.TestStep{
			{
				Config: routerConfig(f, appOnly),
				ConfigStateChecks: []statecheck.StateCheck{
					routerCheck("external_ip", knownvalue.StringExact("203.0.113.1")),
					routerCheck("external_network", knownvalue.StringExact("public-network-02")),
					routerCheck("networks", knownvalue.SetSizeExact(1)),
					routerCheck("enabled", knownvalue.Bool(true)),
					routerCheck("description", knownvalue.Null()),
					routerCheck("external_id", knownvalue.NotNull()),
				},
			},
			{
				// The project isn't echoed: import takes it from the router's network.
				ResourceName: "mtncloud_router.edge", ImportState: true, ImportStateVerify: true,
				ImportStateVerifyIgnore: []string{"timeouts"},
			},
			{
				// A second network is added in place; the gateway, and so the external IP, stay.
				Config: routerConfig(f, both+`
  external_network = "public-network-02"`),
				ConfigPlanChecks: routerPlan(plancheck.ResourceActionUpdate,
					plancheck.ExpectKnownValue("mtncloud_router.edge", tfjsonpath.New("external_ip"), knownvalue.StringExact("203.0.113.1"))),
				ConfigStateChecks: []statecheck.StateCheck{routerCheck("networks", knownvalue.SetSizeExact(2))},
			},
			{
				// Another gateway, spelled in another case: moved in place, with a new external IP.
				Config: routerConfig(f, both+`
  external_network = "PUBLIC-NETWORK-01"`),
				ConfigPlanChecks: routerPlan(plancheck.ResourceActionUpdate,
					plancheck.ExpectUnknownValue("mtncloud_router.edge", tfjsonpath.New("external_ip"))),
				ConfigStateChecks: []statecheck.StateCheck{
					routerCheck("external_ip", knownvalue.StringExact("203.0.113.2")),
					routerCheck("external_network", knownvalue.StringExact("PUBLIC-NETWORK-01")),
				},
			},
			{
				// No gateway: no external IP, although the API still sends the old one.
				Config:            routerConfig(f, both+"\n  description = \"edge router\"\n  enabled = false"),
				ConfigPlanChecks:  routerPlan(plancheck.ResourceActionUpdate),
				ConfigStateChecks: []statecheck.StateCheck{routerCheck("external_ip", knownvalue.Null()), routerCheck("enabled", knownvalue.Bool(false))},
			},
			{
				// A network detached outside Terraform is attached again.
				PreConfig: func() {
					id := edge()
					routers.mu.Lock()
					routers.routers[id]["interfaces"] = routers.routers[id]["interfaces"].([]any)[:1]
					routers.mu.Unlock()
				},
				Config:           routerConfig(f, both+"\n  description = \"edge router\"\n  enabled = false"),
				ConfigPlanChecks: routerPlan(plancheck.ResourceActionUpdate),
			},
			{
				// The API refuses renames, so a new name replaces the router.
				Config:           routerConfig(f, strings.Replace(both, `"edge"`, `"edge-2"`, 1)),
				ConfigPlanChecks: routerPlan(plancheck.ResourceActionDestroyBeforeCreate),
			},
			{
				// Deleted outside Terraform: planned again.
				PreConfig: func() {
					id := routers.byName("edge-2")
					routers.mu.Lock()
					delete(routers.routers, id)
					routers.mu.Unlock()
				},
				Config:           routerConfig(f, strings.Replace(both, `"edge"`, `"edge-2"`, 1)),
				ConfigPlanChecks: routerPlan(plancheck.ResourceActionCreate),
			},
		},
	})
	// Destroy removed the router before its networks; a network still attached would have failed.
	if len(routers.routers) != 0 || len(nets.networks) != 0 {
		t.Errorf("%d routers and %d networks left after destroy", len(routers.routers), len(nets.networks))
	}
}

func TestRouter_Refusals(t *testing.T) {
	f, nets, routers := newRouterFakes(t)
	base := `  name = "edge"
  networks = [mtncloud_network.app.id]`
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories(),
		Steps: []resource.TestStep{
			{
				Config:      routerConfig(f, base+"\n  external_network = \"public-network-09\""),
				PlanOnly:    true,
				ExpectError: regexp.MustCompile(`no external network named "public-network-09"`),
			},
			{
				Config: routerConfig(f, `  name = "edge"
  networks = [mtncloud_network.app.id, mtncloud_network.other.id]`),
				ExpectError: regexp.MustCompile(`Network in another project`),
			},
			{
				// A router already called that is never adopted.
				PreConfig: func() {
					routers.mu.Lock()
					routers.routers[9999] = map[string]any{"id": 9999, "name": "edge", "status": "ok", "interfaces": []any{}}
					routers.mu.Unlock()
				},
				Config:      routerConfig(f, base),
				ExpectError: regexp.MustCompile(`Router already exists`),
			},
			{
				// The same name in another project is a different router: created.
				PreConfig: func() {
					other := nets.byName("other-net")
					routers.mu.Lock()
					routers.routers[9999]["interfaces"] = []any{map[string]any{"network": map[string]any{"id": other}}}
					routers.mu.Unlock()
				},
				Config: routerConfig(f, base),
			},
			{
				PreConfig: func() { routers.mu.Lock(); delete(routers.routers, 9999); routers.mu.Unlock() },
				Config:    f.providerConfig(),
			},
		},
	})
	if routers.creates != 1 {
		t.Errorf("%d create calls, want 1 (the other project's name)", routers.creates)
	}
}

func TestRouter_Failures(t *testing.T) {
	f, _, routers := newRouterFakes(t)
	lock := func(fn func()) func() { return func() { routers.mu.Lock(); fn(); routers.mu.Unlock() } }
	cfg := routerConfig(f, `  name = "edge"
  networks = [mtncloud_network.app.id]`)
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories(),
		Steps: []resource.TestStep{
			{
				// The reply is lost: the router is found and kept, not made twice.
				PreConfig:         lock(func() { routers.lostReplies = 1 }),
				Config:            cfg,
				ConfigStateChecks: []statecheck.StateCheck{routerCheck("networks", knownvalue.SetSizeExact(1))},
			},
			{
				PreConfig: lock(func() {
					if len(routers.routers) != 1 {
						t.Errorf("%d routers after a lost reply, want 1", len(routers.routers))
					}
				}),
				Config: f.providerConfig(),
			},
			{
				// Failed: reported and kept in state, so the next apply replaces it.
				PreConfig:   lock(func() { routers.fail = true }),
				Config:      cfg,
				ExpectError: regexp.MustCompile(`Router create failed`),
			},
			{
				PreConfig:        lock(func() { routers.fail = false }),
				Config:           cfg,
				ConfigPlanChecks: routerPlan(plancheck.ResourceActionDestroyBeforeCreate),
			},
			{
				// Stuck: the create timeout ends the wait with the last status.
				PreConfig: lock(func() { routers.stuck = true }),
				Config: strings.Replace(cfg, `name = "edge"`, `name = "edge-slow"
  timeouts {
    create = "2s"
  }`, 1),
				ExpectError: regexp.MustCompile(`(?s)still "provisioning".*after the create timeout`),
			},
			{
				PreConfig: lock(func() { routers.stuck = false }),
				Config:    f.providerConfig(),
			},
			{
				// The timeout ends the wait for the create's reply: the router is found and kept.
				PreConfig: lock(func() { routers.slowReply = 5 * time.Second }),
				Config: strings.Replace(cfg, `name = "edge"`, `name = "edge"
  timeouts {
    create = "1s"
  }`, 1),
				ExpectError: regexp.MustCompile(`Timed out waiting for the router`),
			},
			{
				PreConfig: lock(func() {
					routers.slowReply = 0
					if len(routers.routers) != 1 {
						t.Errorf("%d routers after a timed-out create, want 1 (kept in state, not orphaned)", len(routers.routers))
					}
				}),
				Config:           cfg,
				ConfigPlanChecks: routerPlan(plancheck.ResourceActionDestroyBeforeCreate),
			},
		},
	})
	if len(routers.routers) != 0 {
		t.Errorf("%d routers left after destroy", len(routers.routers))
	}
}

func TestRouter_ImportWithoutNetworks(t *testing.T) {
	f, _, routers := newRouterFakes(t)
	routers.routers[9990] = map[string]any{"id": 9990, "name": "edge", "status": "ok", "enabled": true, "interfaces": []any{},
		"externalId": "00000000-0000-0000-0000-000000009990"}
	cfg := routerConfig(f, `  name = "edge"
  networks = [mtncloud_network.app.id]`)
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories(),
		Steps: []resource.TestStep{
			{ResourceName: "mtncloud_router.edge", ImportState: true, ImportStateId: "9990", ImportStatePersist: true, Config: cfg},
			{
				// No network told the project: the configured one is adopted, and the network is attached in place.
				Config:           cfg,
				ConfigPlanChecks: routerPlan(plancheck.ResourceActionUpdate),
			},
		},
	})
}

// The router's project from the provider, and the same project by its full name on the router:
// neither is a change, though the router's computed values aren't kept from state by the schema.
func TestRouter_ProviderProject(t *testing.T) {
	f, _, _ := newRouterFakes(t)
	cfg := func(provider, routerProject string) string {
		c := strings.Replace(routerConfig(f, `  name = "edge"
  networks = [mtncloud_network.app.id]`), `  project = "web-prod"
`+"` + attrs + `", "", 1)
		c = strings.Replace(c, "resource \"mtncloud_router\" \"edge\" {\n  project = \"web-prod\"\n", "resource \"mtncloud_router\" \"edge\" {\n"+routerProject, 1)
		if provider != "" {
			c = strings.Replace(c, `access_token = "test-token"`, `access_token = "test-token"
  project      = "`+provider+`"`, 1)
		}
		return c
	}
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories(),
		Steps: []resource.TestStep{
			{Config: cfg("", ""), ExpectError: regexp.MustCompile(`(?s)No project set.*MTNCLOUD_PROJECT`)},
			{Config: cfg("web-prod", ""), ConfigStateChecks: []statecheck.StateCheck{routerCheck("project", knownvalue.StringExact("web-prod"))}},
			{Config: cfg("web-prod-Acme", ""), ConfigPlanChecks: routerPlan(plancheck.ResourceActionNoop)},
			{Config: cfg("", "  project = \"web-prod-Acme\"\n"), ConfigPlanChecks: routerPlan(plancheck.ResourceActionNoop),
				ConfigStateChecks: []statecheck.StateCheck{routerCheck("project", knownvalue.StringExact("web-prod"))}},
			{Config: cfg("web-prod-Acme", ""), ResourceName: "mtncloud_router.edge", ImportState: true, ImportStateKind: resource.ImportBlockWithID},
		},
	})
}
