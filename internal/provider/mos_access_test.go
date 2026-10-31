package provider

import (
	"encoding/json"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/knownvalue"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/statecheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"
)

// mosCypher fakes Cypher: the list shows the keys, a GET reads one.
type mosCypher struct {
	mu   sync.Mutex
	keys map[string]string
}

func (c *mosCypher) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if r.URL.Path == "/api/cypher" {
		list := []any{}
		for k := range c.keys {
			if strings.HasPrefix(k, r.URL.Query().Get("phrase")) {
				list = append(list, map[string]any{"itemKey": k})
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"cyphers": list, "meta": map[string]any{"total": len(list)}})
		return
	}
	v, ok := c.keys[strings.TrimPrefix(r.URL.Path, "/api/cypher/")]
	if !ok {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"success":false,"msg":"not found"}`))
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "data": v})
}

// mosKeys is what MTN's MOS workflow writes for user jdoe of tenant Acme.
func mosKeys(access string) map[string]string {
	p := "secret/MOS-acme_jdoe_"
	return map[string]string{p + "access_key": access, p + "secret_key": "s-" + access,
		p + "public_endpoint_url": "https://acme.mos.example", p + "private_endpoint_url": "https://acme.mos.internal"}
}

func mosAccessFakes(t *testing.T, keys map[string]string) (*fakeAPI, *orderStore, *mosCypher) {
	t.Helper()
	f, s := newOrderFakes(t)
	cy := &mosCypher{keys: keys}
	f.cypher = cy
	rotations := 0
	s.made = func(map[string]any) { // the workflow makes new keys, replacing any
		rotations++
		cy.mu.Lock()
		defer cy.mu.Unlock()
		cy.keys = mosKeys("ak-" + string(rune('0'+rotations)))
	}
	return f, s, cy
}

func accessConfig(f *fakeAPI, body string) string {
	return f.providerConfig() + "resource \"mtncloud_mos_access\" \"k\" {\n" + body + "\n}\n"
}

func accessAttr(a string, v knownvalue.Check) statecheck.StateCheck {
	return statecheck.ExpectKnownValue("mtncloud_mos_access.k", tfjsonpath.New(a), v)
}

// The tenant has keys: they are adopted and nothing is ordered, until rotate_trigger changes.
// Destroy leaves them.
func TestMOSAccess_Adopts(t *testing.T) {
	f, s, cy := mosAccessFakes(t, mosKeys("ak-0"))
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories(),
		CheckDestroy: func(*terraform.State) error {
			if len(cy.keys) == 0 {
				t.Error("destroy removed the keys")
			}
			return nil
		},
		Steps: []resource.TestStep{
			{Config: accessConfig(f, `  rotate_trigger = "a"`), ConfigStateChecks: []statecheck.StateCheck{
				accessAttr("id", knownvalue.StringExact("secret/MOS-acme_jdoe_")),
				accessAttr("source", knownvalue.StringExact("adopted")),
				accessAttr("order_id", knownvalue.StringExact("")),
				accessAttr("public_endpoint", knownvalue.StringExact("https://acme.mos.example")),
			}, Check: func(*terraform.State) error {
				if s.orders != 0 {
					t.Errorf("%d orders, want none: the tenant has keys", s.orders)
				}
				return nil
			}},
			{Config: accessConfig(f, `  rotate_trigger = "a"`), ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()}}},
			// rotate only counts at create.
			{Config: accessConfig(f, "  rotate_trigger = \"a\"\n  rotate = true"), Check: func(*terraform.State) error {
				if s.orders != 0 {
					t.Errorf("%d orders after setting rotate on an existing resource, want none", s.orders)
				}
				return nil
			}},
			{Config: accessConfig(f, "  rotate_trigger = \"b\"\n  rotate = true"), ConfigStateChecks: []statecheck.StateCheck{
				accessAttr("source", knownvalue.StringExact("ordered")),
				accessAttr("order_id", knownvalue.StringExact("9801")),
			}, Check: func(*terraform.State) error {
				if s.orders != 1 || cy.keys["secret/MOS-acme_jdoe_access_key"] != "ak-1" {
					t.Errorf("%d orders, key %q; want one rotation", s.orders, cy.keys["secret/MOS-acme_jdoe_access_key"])
				}
				return nil
			}},
			{ResourceName: "mtncloud_mos_access.k", ImportState: true, ImportStateId: "mos", ImportStateVerify: true,
				ImportStateVerifyIgnore: []string{"rotate", "rotate_trigger", "source", "order_id"}},
		},
	})
}

// No keys: create orders them. rotate = true with keys: create orders new ones.
func TestMOSAccess_Orders(t *testing.T) {
	for _, tc := range []struct {
		name string
		keys map[string]string
		body string
	}{
		{"no keys", map[string]string{}, ""},
		{"rotate", mosKeys("ak-0"), "  rotate = true"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, s, cy := mosAccessFakes(t, tc.keys)
			resource.UnitTest(t, resource.TestCase{
				ProtoV6ProviderFactories: factories(),
				Steps: []resource.TestStep{
					{Config: accessConfig(f, tc.body), ConfigStateChecks: []statecheck.StateCheck{
						accessAttr("source", knownvalue.StringExact("ordered")),
						accessAttr("order_id", knownvalue.StringExact("9801")),
						accessAttr("private_endpoint", knownvalue.StringExact("https://acme.mos.internal")),
					}, Check: func(*terraform.State) error {
						if s.orders != 1 || cy.keys["secret/MOS-acme_jdoe_access_key"] != "ak-1" {
							t.Errorf("%d orders, want 1", s.orders)
						}
						return nil
					}},
					{Config: accessConfig(f, tc.body), ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()}}},
				},
			})
		})
	}
}

// A failed order says why, and keeps nothing.
func TestMOSAccess_OrderFails(t *testing.T) {
	f, s, _ := mosAccessFakes(t, map[string]string{})
	s.result, s.failure = "error", "Storage cluster unreachable"
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories(),
		Steps: []resource.TestStep{{Config: accessConfig(f, ""),
			ExpectError: regexp.MustCompile(`(?s)order 9801 failed.*Storage cluster unreachable`)}},
	})
}

func ordersWant(t *testing.T, s *orderStore, want int, why string) func(*terraform.State) error {
	return func(*terraform.State) error {
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.orders != want {
			t.Errorf("%s: %d orders, want %d", why, s.orders, want)
		}
		return nil
	}
}

// Only a change from one value to another rotates: setting the trigger where it was unset (as the
// first apply after an import does) and removing it order nothing.
func TestMOSAccess_TriggerRules(t *testing.T) {
	f, s, _ := mosAccessFakes(t, mosKeys("ak-0"))
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories(),
		Steps: []resource.TestStep{
			{Config: accessConfig(f, ""), ResourceName: "mtncloud_mos_access.k", ImportState: true, ImportStateId: "mos",
				ImportStatePersist: true, ImportStateCheck: func([]*terraform.InstanceState) error { return nil }},
			{Config: accessConfig(f, `  rotate_trigger = "2026-11"`), Check: ordersWant(t, s, 0, "trigger set after an import")},
			{Config: accessConfig(f, ""), Check: ordersWant(t, s, 0, "trigger removed")},
			{Config: accessConfig(f, `  rotate_trigger = "2026-12"`), Check: ordersWant(t, s, 0, "trigger set again")},
			{Config: accessConfig(f, `  rotate_trigger = "2027-01"`), Check: ordersWant(t, s, 1, "trigger changed")},
		},
	})
}

// A trigger known only at apply plans what a rotation changes, so the rotation it may bring is
// consistent with the plan.
func TestMOSAccess_UnknownTrigger(t *testing.T) {
	f, s, _ := mosAccessFakes(t, mosKeys("ak-0"))
	cfg := func(v string) string {
		return `resource "terraform_data" "t" { input = "` + v + `" }` + "\n" + accessConfig(f, "  rotate_trigger = terraform_data.t.output")
	}
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories(),
		Steps: []resource.TestStep{
			{Config: cfg("a"), Check: ordersWant(t, s, 0, "create")},
			{Config: cfg("b"), ConfigStateChecks: []statecheck.StateCheck{accessAttr("source", knownvalue.StringExact("ordered"))},
				Check: ordersWant(t, s, 1, "trigger changed at apply")},
		},
	})
}

// An order that doesn't finish in time is saved, not failed: ordering again would rotate a second
// time. A refresh keeps it until the keys appear.
func TestMOSAccess_OrderPending(t *testing.T) {
	for _, tc := range []struct {
		name, body, id string
		keys           map[string]string
	}{
		{"no keys", "", "order-9801", map[string]string{}},
		{"rotate", "  rotate = true", "secret/MOS-acme_jdoe_", mosKeys("ak-0")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, s, _ := mosAccessFakes(t, tc.keys)
			s.result = "" // the workflow never reports
			cfg := accessConfig(f, tc.body+"\n  timeouts {\n    create = \"3s\"\n  }")
			resource.UnitTest(t, resource.TestCase{
				ProtoV6ProviderFactories: factories(),
				Steps: []resource.TestStep{
					{Config: cfg, ConfigStateChecks: []statecheck.StateCheck{
						accessAttr("id", knownvalue.StringExact(tc.id)),
						accessAttr("order_id", knownvalue.StringExact("9801")),
					}, Check: ordersWant(t, s, 1, "first apply")},
					{Config: cfg, ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()}},
						Check: ordersWant(t, s, 1, "second apply")},
				},
			})
		})
	}
}
