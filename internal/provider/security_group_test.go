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

// sgStore is an in-memory security group API that answers the way MTN Cloud did in the live test
// (dev-docs/api-research.md): rules made through the API come back as "customRule" with the
// remote in "source"; the two default egress rules arrive later, synced, as "custom".
type sgStore struct {
	mu       sync.Mutex
	nextID   int64
	groups   map[int64]map[string]any
	projects map[int64]string
	// lostReplies makes the next N creates succeed but answer 502, as when a gateway times out.
	lostReplies int
}

func newSGStore() *sgStore {
	return &sgStore{nextID: 9500, groups: map[int64]map[string]any{},
		projects: map[int64]string{9201: "web-prod-Acme", 9202: "data-Acme"}}
}

func (s *sgStore) id() int64 { s.nextID++; return s.nextID }

func (s *sgStore) byName(name string) int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, g := range s.groups {
		if g["name"] == name {
			return id
		}
	}
	return 0
}

func (s *sgStore) addRule(groupID int64, rule map[string]any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rule["id"] = s.id()
	g := s.groups[groupID]
	g["rules"] = append(g["rules"].([]any), rule)
}

// sync adds the default egress rules OpenStack creates, as MTN Cloud's sync reports them.
func (s *sgStore) sync(groupID int64) {
	for _, cidr := range []string{"0.0.0.0/0", "::/0"} {
		s.addRule(groupID, map[string]any{"ruleType": "custom", "customRule": true, "direction": "egress", "protocol": "any",
			"sourceType": "all", "source": nil, "sourceGroup": nil, "portRange": nil, "destinationType": "instance",
			"destination": cidr, "name": nil, "externalId": "00000000-0000-0000-0000-00000000" + strconv.Itoa(len(cidr))})
	}
}

func (s *sgStore) remove(groupID int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.groups, groupID)
}

func (s *sgStore) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/security-groups"), "/"), "/")
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	reply := func(status int, v any) {
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(v)
	}
	notFound := func() {
		reply(http.StatusNotFound, map[string]any{"success": false, "msg": "Security Group not found"})
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	var g map[string]any
	var gid int64
	if parts[0] != "" {
		gid, _ = strconv.ParseInt(parts[0], 10, 64)
		if g = s.groups[gid]; g == nil {
			notFound()
			return
		}
	}
	switch {
	case r.Method == http.MethodPost && parts[0] == "":
		in := body["securityGroup"].(map[string]any)
		pool := int64(in["customOptions"].(map[string]any)["resourcePoolId"].(float64))
		id := s.id()
		uuid := fmt.Sprintf("00000000-0000-0000-0000-%012d", id)
		g = map[string]any{"id": id, "name": in["name"], "description": in["description"], "externalId": uuid,
			"locations": []any{map[string]any{"id": id + 100, "externalId": uuid, "status": "available",
				"zonePool": map[string]any{"id": pool, "name": s.projects[pool]}}},
			"rules": []any{}}
		s.groups[id] = g
		if s.lostReplies > 0 {
			s.lostReplies--
			reply(http.StatusBadGateway, map[string]any{"success": false, "msg": "Bad Gateway"})
			return
		}
		reply(http.StatusOK, map[string]any{"securityGroup": g, "success": true})
	case r.Method == http.MethodGet && parts[0] == "":
		list := []any{}
		for _, x := range s.groups {
			if name := r.URL.Query().Get("name"); name == "" || x["name"] == name {
				list = append(list, x)
			}
		}
		reply(http.StatusOK, map[string]any{"securityGroups": list, "meta": map[string]any{"total": len(list)}})
	case r.Method == http.MethodGet && len(parts) == 1:
		reply(http.StatusOK, map[string]any{"securityGroup": g})
	case r.Method == http.MethodPut && len(parts) == 1:
		in := body["securityGroup"].(map[string]any)
		g["name"], g["description"] = in["name"], in["description"]
		reply(http.StatusOK, map[string]any{"success": true})
	case r.Method == http.MethodDelete && len(parts) == 1:
		delete(s.groups, gid)
		reply(http.StatusOK, map[string]any{"success": true})
	case r.Method == http.MethodPost && len(parts) == 2 && parts[1] == "rules":
		in := body["rule"].(map[string]any)
		rule := map[string]any{"id": s.id(), "ruleType": "customRule", "customRule": true, "name": in["name"],
			"direction": in["direction"], "protocol": in["protocol"], "portRange": in["portRange"],
			"sourceType": in["sourceType"], "source": in["source"], "sourceGroup": nil,
			"destinationType": "instance", "destination": nil, "destinationPortRange": "*", "externalId": nil}
		if sg, ok := in["sourceGroup"].(map[string]any); ok {
			rule["sourceGroup"] = map[string]any{"id": sg["id"], "name": "peer"}
			rule["source"] = "198.51.100.0/24" // the API fills source for group rules too
		}
		g["rules"] = append(g["rules"].([]any), rule)
		if s.lostReplies > 0 {
			s.lostReplies--
			reply(http.StatusBadGateway, map[string]any{"success": false, "msg": "Bad Gateway"})
			return
		}
		reply(http.StatusOK, map[string]any{"rule": rule, "success": true})
	case r.Method == http.MethodDelete && len(parts) == 3 && parts[1] == "rules":
		rid, _ := strconv.ParseInt(parts[2], 10, 64)
		var kept []any
		found := false
		for _, x := range g["rules"].([]any) {
			if x.(map[string]any)["id"] == rid {
				found = true
				continue
			}
			kept = append(kept, x)
		}
		if !found {
			notFound()
			return
		}
		if kept == nil {
			kept = []any{}
		}
		g["rules"] = kept
		reply(http.StatusOK, map[string]any{"success": true})
	default:
		reply(http.StatusNotFound, map[string]any{"success": false, "msg": "no route in fake API"})
	}
}

func sgConfig(f *fakeAPI, groupAttrs string) string {
	return f.providerConfig() + `
resource "mtncloud_security_group" "web" {
  name    = "web"
  ` + groupAttrs + `
}

resource "mtncloud_security_group" "other" {
  name    = "other"
  project = "data"
}

resource "mtncloud_security_group_rule" "ping" {
  security_group_id = mtncloud_security_group.other.id
  direction         = "ingress"
  protocol          = "icmp"
  cidr              = "10.0.0.0/8"
}
`
}

const (
	webRules = `project     = "web-prod"
  description = "web servers"
  rules = [
    { direction = "ingress", protocol = "tcp", port_range = "22", cidr = "10.0.0.0/24", description = "ssh" },
    { direction = "egress", protocol = "tcp", port_range = "443", cidr = "192.0.2.0/24" },
  ]`
	webRulesChanged = `project     = "web-prod-Acme"
  description = "web servers"
  rules = [
    { direction = "ingress", protocol = "tcp", port_range = "22", cidr = "10.0.0.0/24", description = "ssh" },
    { direction = "ingress", protocol = "tcp", port_range = "443", cidr = "0.0.0.0/0" },
  ]`
)

func allRules(n int) statecheck.StateCheck {
	return statecheck.ExpectKnownValue("mtncloud_security_group.web", tfjsonpath.New("all_rules"), knownvalue.ListSizeExact(n))
}

// The full life cycle against the fake API: every apply is followed by an empty plan.
func TestSecurityGroup_Lifecycle(t *testing.T) {
	f := newFakeAPI(t)
	store := newSGStore()
	f.sg = store
	web := func() int64 { return store.byName("web") }

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories(),
		Steps: []resource.TestStep{
			{
				Config: sgConfig(f, webRules),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue("mtncloud_security_group.web", tfjsonpath.New("project"), knownvalue.StringExact("web-prod")),
					statecheck.ExpectKnownValue("mtncloud_security_group.web", tfjsonpath.New("rules"), knownvalue.SetSizeExact(2)),
					statecheck.ExpectKnownValue("mtncloud_security_group.web", tfjsonpath.New("remove_default_egress"), knownvalue.Bool(false)),
					statecheck.ExpectKnownValue("mtncloud_security_group.other", tfjsonpath.New("rules"), knownvalue.Null()),
					statecheck.ExpectKnownValue("mtncloud_security_group_rule.ping", tfjsonpath.New("description"), knownvalue.Null()),
					allRules(2),
				},
			},
			{
				// The sync brings the default egress rules: they are listed, and left alone.
				PreConfig:        func() { store.sync(web()) },
				Config:           sgConfig(f, webRules),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()}},
				ConfigStateChecks: []statecheck.StateCheck{
					allRules(4),
					statecheck.ExpectKnownValue("mtncloud_security_group.web", tfjsonpath.New("rules"), knownvalue.SetSizeExact(2)),
				},
			},
			{ResourceName: "mtncloud_security_group.web", ImportState: true, ImportStateVerify: true},
			{ResourceName: "mtncloud_security_group_rule.ping", ImportState: true, ImportStateVerify: true},
			{
				// Opting in removes them on this apply.
				Config: sgConfig(f, webRules+"\n  remove_default_egress = true"),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction("mtncloud_security_group.web", plancheck.ResourceActionUpdate),
				}},
				ConfigStateChecks: []statecheck.StateCheck{allRules(2)},
			},
			{
				// New rules, and the same project written by its full name: an update, not a replacement.
				Config: sgConfig(f, webRulesChanged+"\n  remove_default_egress = true"),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction("mtncloud_security_group.web", plancheck.ResourceActionUpdate),
				}},
				ConfigStateChecks: []statecheck.StateCheck{
					allRules(2),
					statecheck.ExpectKnownValue("mtncloud_security_group.web", tfjsonpath.New("project"), knownvalue.StringExact("web-prod-Acme")),
				},
			},
			{
				// Already opted in, and the defaults sync in later: the next plan removes them.
				// Nothing else changed, so only ModifyPlan can produce this update.
				PreConfig: func() { store.sync(web()) },
				Config:    sgConfig(f, webRulesChanged+"\n  remove_default_egress = true"),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction("mtncloud_security_group.web", plancheck.ResourceActionUpdate),
				}},
				ConfigStateChecks: []statecheck.StateCheck{allRules(2)},
			},
			{
				// A rule added outside Terraform is drift, and is removed.
				PreConfig: func() {
					store.addRule(web(), map[string]any{"ruleType": "customRule", "direction": "ingress", "protocol": "udp",
						"portRange": "53", "sourceType": "cidr", "source": "0.0.0.0/0", "destinationType": "instance", "name": "dns"})
				},
				Config: sgConfig(f, webRulesChanged+"\n  remove_default_egress = true"),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction("mtncloud_security_group.web", plancheck.ResourceActionUpdate),
				}},
				ConfigStateChecks: []statecheck.StateCheck{allRules(2)},
			},
			{
				// Deleted outside Terraform: planned again, never an error.
				PreConfig: func() { store.remove(web()) },
				Config:    sgConfig(f, webRulesChanged+"\n  remove_default_egress = true"),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction("mtncloud_security_group.web", plancheck.ResourceActionCreate),
				}},
			},
			{
				// Another project replaces the group.
				Config: sgConfig(f, strings.Replace(webRulesChanged, `"web-prod-Acme"`, `"data"`, 1)),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction("mtncloud_security_group.web", plancheck.ResourceActionDestroyBeforeCreate),
				}},
			},
		},
	})
	if n := len(store.groups); n != 0 {
		t.Errorf("%d security groups left after destroy", n)
	}
}

func TestSecurityGroup_PlanTimeErrors(t *testing.T) {
	f := newFakeAPI(t)
	f.sg = newSGStore()
	for name, tc := range map[string]struct{ rules, want string }{
		"port on icmp":         {`{ direction = "ingress", protocol = "icmp", port_range = "22" }`, `A icmp rule applies to every port`},
		"default egress":       {`{ direction = "egress", protocol = "any", cidr = "0.0.0.0/0" }`, `This is a default egress rule`},
		"host bits in cidr":    {`{ direction = "ingress", protocol = "tcp", cidr = "10.0.0.5/24" }`, `Use "10.0.0.0/24"`},
		"cidr and group":       {`{ direction = "ingress", protocol = "tcp", cidr = "10.0.0.0/24", source_security_group_id = "7" }`, `cannot be specified when`},
		"reversed port range":  {`{ direction = "ingress", protocol = "tcp", port_range = "8080-8000" }`, `put the lower port first`},
		"unknown protocol":     {`{ direction = "ingress", protocol = "sctp" }`, `value must be one of`},
		"single port as range": {`{ direction = "ingress", protocol = "tcp", port_range = "22-22" }`, `write a single port as "22"`},
		"duplicate": {`{ direction = "ingress", protocol = "tcp", port_range = "22", cidr = "10.0.0.0/24", description = "a" },
		               { direction = "ingress", protocol = "tcp", port_range = "22", cidr = "10.0.0.0/24" }`, `Duplicate rule`},
		"group id zero":   {`{ direction = "ingress", protocol = "tcp", source_security_group_id = "0" }`, `must be a security group id`},
		"ipv6 upper case": {`{ direction = "ingress", protocol = "tcp", cidr = "2001:DB8::/32" }`, `as "2001:db8::/32"`},
	} {
		t.Run(name, func(t *testing.T) {
			resource.UnitTest(t, resource.TestCase{
				ProtoV6ProviderFactories: factories(),
				Steps: []resource.TestStep{{
					Config: f.providerConfig() + `
resource "mtncloud_security_group" "x" {
  name    = "x"
  project = "web-prod"
  rules   = [` + tc.rules + `]
}`,
					PlanOnly:    true,
					ExpectError: regexp.MustCompile(regexp.QuoteMeta(tc.want)),
				}},
			})
		})
	}
}

func TestSecurityGroup_UnknownProjectAtPlan(t *testing.T) {
	f := newFakeAPI(t)
	f.sg = newSGStore()
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories(),
		Steps: []resource.TestStep{{
			Config: f.providerConfig() + `
resource "mtncloud_security_group" "x" {
  name    = "x"
  project = "web-prd"
}`,
			PlanOnly:    true,
			ExpectError: regexp.MustCompile(`Unknown project`),
		}},
	})
}

// A 502 after the platform made the group or rule must not leave it outside Terraform, and a
// group that was already there is never adopted.
func TestSecurityGroup_LostRepliesAndExisting(t *testing.T) {
	f := newFakeAPI(t)
	store := newSGStore()
	f.sg = store
	cfg := f.providerConfig() + `
resource "mtncloud_security_group" "web" {
  name    = "web"
  project = "web-prod"
  rules   = [{ direction = "ingress", protocol = "tcp", port_range = "22", cidr = "10.0.0.0/24" }]
}

resource "mtncloud_security_group" "loose" {
  name    = "loose"
  project = "web-prod"
}

resource "mtncloud_security_group_rule" "ping" {
  security_group_id = mtncloud_security_group.loose.id
  direction         = "egress"
  protocol          = "icmp"
  cidr              = "10.0.0.0/8"
}
`
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories(),
		Steps: []resource.TestStep{
			{
				// Group create, its first rule and the rule resource each lose their reply.
				PreConfig: func() { store.mu.Lock(); store.lostReplies = 1; store.mu.Unlock() },
				Config:    cfg,
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue("mtncloud_security_group.web", tfjsonpath.New("rules"), knownvalue.SetSizeExact(1)),
				},
			},
			{
				PreConfig: func() {
					if n := len(store.groups); n != 2 {
						t.Errorf("%d groups after a lost reply, want 2", n)
					}
					store.mu.Lock()
					store.lostReplies = 1 // now the rule resource's create (replaced below) loses its reply
					store.mu.Unlock()
				},
				Config: strings.Replace(cfg, `"10.0.0.0/8"`, `"10.1.0.0/16"`, 1),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction("mtncloud_security_group_rule.ping", plancheck.ResourceActionDestroyBeforeCreate),
				}},
			},
			{
				// A second "web" in the same project is refused, pointing at import.
				Config: cfg + `
resource "mtncloud_security_group" "dup" {
  name    = "web"
  project = "web-prod"
  depends_on = [mtncloud_security_group.web]
}`,
				ExpectError: regexp.MustCompile(`Security group already exists`),
			},
		},
	})
}
