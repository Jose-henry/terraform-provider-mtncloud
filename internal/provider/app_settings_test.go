package provider

import (
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strconv"
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

// appStore fakes one app a managed service order made (GET/PUT /api/apps/{id}, add- and
// remove-instance) and the tenant's users, as MTN Cloud shows them (2026-10-31).
type appStore struct {
	mu      sync.Mutex
	exists  bool
	app     map[string]any
	tiers   map[int64]string // instance -> tier
	puts    []map[string]any // each PUT's "app" body
	removed []int64
	// Blank apps made with POST /api/apps (mtncloud_app), by ID, with their tiers.
	made      map[int64]map[string]any
	madeTiers map[int64]map[int64]string
	posts     []map[string]any
	deletes   []string // "<id> removeInstances=<value>"
}

func newAppStore(f *fakeAPI) *appStore {
	s := &appStore{exists: true, made: map[int64]map[string]any{}, madeTiers: map[int64]map[int64]string{}, tiers: map[int64]string{9701: "Master"}, app: map[string]any{
		"id": 9901, "name": "ab-42-MTN-CLOUD-MYSQL", "description": "made by an order", "labels": []any{"order"},
		"environment": "dev", "status": "running", "owner": map[string]any{"id": 7, "username": "ab42"},
		"group": map[string]any{"name": "MTNNG_CLOUD_AZ_1"},
	}}
	f.mu.Lock()
	f.app = s
	f.mu.Unlock()
	return s
}

func (s *appStore) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	reply := func(code int, v any) { w.WriteHeader(code); _ = json.NewEncoder(w).Encode(v) }
	if r.URL.Path == "/api/users" {
		users := []any{map[string]any{"id": 7, "username": "ab42"}, map[string]any{"id": 8, "username": "ops-team"}}
		reply(http.StatusOK, map[string]any{"users": users, "meta": map[string]any{"total": len(users)}})
		return
	}
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	if r.URL.Path == "/api/apps" {
		if r.Method == http.MethodPost {
			for _, a := range s.made {
				if strings.EqualFold(a["name"].(string), body["name"].(string)) {
					reply(http.StatusBadRequest, map[string]any{"success": false, "errors": map[string]any{"name": "must be unique"}})
					return
				}
			}
			s.posts = append(s.posts, body)
			id := int64(9950 + len(s.posts))
			a := map[string]any{"id": id, "name": body["name"], "description": body["description"], "labels": body["labels"],
				"environment": body["environment"], "status": "running", "group": map[string]any{"name": "MTNNG_CLOUD_AZ_1"}}
			s.made[id], s.madeTiers[id] = a, map[int64]string{}
			reply(http.StatusOK, map[string]any{"success": true, "app": map[string]any{"id": id}})
			return
		}
		list := []any{}
		if s.exists {
			list = append(list, map[string]any{"id": 9901, "name": s.app["name"], "status": "running"})
		}
		for id, a := range s.made {
			list = append(list, map[string]any{"id": id, "name": a["name"], "status": "running"})
		}
		reply(http.StatusOK, map[string]any{"apps": list, "meta": map[string]any{"total": len(list)}})
		return
	}
	rest := strings.TrimPrefix(r.URL.Path, "/api/apps/")
	idPart, action, _ := strings.Cut(rest, "/")
	id, _ := strconv.ParseInt(idPart, 10, 64)
	app, tiers := s.app, s.tiers
	switch {
	case id == 9901 && s.exists:
	case s.made[id] != nil:
		app, tiers = s.made[id], s.madeTiers[id]
	default:
		reply(http.StatusNotFound, map[string]any{"success": false, "msg": "App not found"})
		return
	}
	if r.Method == http.MethodDelete {
		s.deletes = append(s.deletes, fmt.Sprintf("%d removeInstances=%s", id, r.URL.Query().Get("removeInstances")))
		if id == 9901 {
			s.exists = false
		} else {
			delete(s.made, id)
		}
		reply(http.StatusOK, map[string]any{"success": true})
		return
	}
	switch {
	case r.Method == http.MethodGet && action == "":
		out := map[string]any{}
		for k, v := range app {
			out[k] = v
		}
		byTier := map[string][]any{}
		for inst, tier := range tiers {
			byTier[tier] = append(byTier[tier], map[string]any{"instance": map[string]any{"id": inst}})
		}
		var appTiers []any
		for tier, insts := range byTier {
			appTiers = append(appTiers, map[string]any{"tier": map[string]any{"name": tier}, "appInstances": insts})
		}
		out["appTiers"] = appTiers
		reply(http.StatusOK, map[string]any{"app": out})
	case r.Method == http.MethodPut && action == "":
		upd, _ := body["app"].(map[string]any)
		s.puts = append(s.puts, upd)
		for k, v := range upd {
			if k == "ownerId" {
				name := map[float64]string{7: "ab42", 8: "ops-team"}[v.(float64)]
				app["owner"] = map[string]any{"id": v, "username": name}
				continue
			}
			app[k] = v
		}
		reply(http.StatusOK, map[string]any{"success": true})
	case r.Method == http.MethodPost && action == "add-instance":
		tiers[int64(body["instanceId"].(float64))] = body["tierName"].(string)
		reply(http.StatusOK, map[string]any{"success": true})
	case r.Method == http.MethodPost && action == "remove-instance":
		inst := int64(body["instanceId"].(float64))
		delete(tiers, inst)
		s.removed = append(s.removed, inst)
		reply(http.StatusOK, map[string]any{"success": true})
	default:
		reply(http.StatusNotFound, map[string]any{"success": false})
	}
}

func (s *appStore) putCount() int { s.mu.Lock(); defer s.mu.Unlock(); return len(s.puts) }

func appSettingsConfig(f *fakeAPI, body string) string {
	return f.providerConfig() + "resource \"mtncloud_app_settings\" \"db\" {\n  app_id = \"9901\"\n" + body + "\n}\n"
}

func appAttr(a string, v knownvalue.Check) statecheck.StateCheck {
	return statecheck.ExpectKnownValue("mtncloud_app_settings.db", tfjsonpath.New(a), v)
}

// Adopting sends nothing; set settings change in place and are held; destroy leaves the app (B-028).
func TestAppSettings_Lifecycle(t *testing.T) {
	f := newFakeAPI(t)
	s := newAppStore(f)
	empty := resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()}}
	update := resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
		plancheck.ExpectResourceAction("mtncloud_app_settings.db", plancheck.ResourceActionUpdate)}}
	managed := `  name        = "reporting-db"
  labels      = ["order", "reporting"]
  environment = "Production"
  owner       = "OPS-team"`
	sameDesc := "\n  description = \"made by an order\"" // set, and already the app's
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories(),
		CheckDestroy: func(*terraform.State) error {
			if !s.exists {
				t.Error("destroy deleted the app: it belongs to the order")
			}
			return nil
		},
		Steps: []resource.TestStep{
			{Config: appSettingsConfig(f, ""), ConfigStateChecks: []statecheck.StateCheck{
				appAttr("name", knownvalue.StringExact("ab-42-MTN-CLOUD-MYSQL")),
				appAttr("environment", knownvalue.StringExact("Dev")),
				appAttr("owner", knownvalue.StringExact("ab42")),
				appAttr("instance_ids", knownvalue.SetExact([]knownvalue.Check{knownvalue.StringExact("9701")})),
			}, Check: func(*terraform.State) error {
				if n := s.putCount(); n != 0 {
					t.Errorf("adopting sent %d updates, want none", n)
				}
				return nil
			}},
			{Config: appSettingsConfig(f, ""), ConfigPlanChecks: empty},
			{Config: appSettingsConfig(f, managed), ConfigPlanChecks: update, ConfigStateChecks: []statecheck.StateCheck{
				appAttr("name", knownvalue.StringExact("reporting-db")),
				appAttr("labels", knownvalue.SetSizeExact(2)),
				appAttr("environment", knownvalue.StringExact("Production")),
				appAttr("owner", knownvalue.StringExact("OPS-team")),
				appAttr("description", knownvalue.StringExact("made by an order")), // not set: kept
			}, Check: func(*terraform.State) error {
				s.mu.Lock()
				defer s.mu.Unlock()
				if len(s.puts) != 1 {
					t.Fatalf("%d updates, want 1", len(s.puts))
				}
				p := s.puts[0]
				if p["environment"] != "production" || p["ownerId"] != 8.0 || p["description"] != nil {
					t.Errorf("update sent %v: want environment by code, owner by ID, and nothing unset", p)
				}
				return nil
			}},
			{Config: appSettingsConfig(f, managed), ConfigPlanChecks: empty},
			// Renamed in the console: the next apply renames it back, and sends nothing else (the
			// description is set but already right).
			{PreConfig: func() { s.mu.Lock(); s.app["name"] = "renamed"; s.mu.Unlock() },
				Config: appSettingsConfig(f, managed+sameDesc), ConfigPlanChecks: update,
				ConfigStateChecks: []statecheck.StateCheck{appAttr("name", knownvalue.StringExact("reporting-db"))},
				Check: func(*terraform.State) error {
					s.mu.Lock()
					defer s.mu.Unlock()
					if last := s.puts[len(s.puts)-1]; len(last) != 1 || last["name"] != "reporting-db" {
						t.Errorf("rename sent %v, want only the name", last)
					}
					return nil
				}},
			// Import reads the server's spellings (the owner's case).
			{Config: appSettingsConfig(f, managed), ResourceName: "mtncloud_app_settings.db", ImportState: true, ImportStateId: "9901",
				ImportStateVerify: true, ImportStateVerifyIgnore: []string{"owner"}},
			{Config: appSettingsConfig(f, managed+sameDesc), ConfigPlanChecks: empty},
		},
	})
}

// The app deleted with its order: the settings leave state, and a plan shows them again.
func TestAppSettings_Disappears(t *testing.T) {
	f := newFakeAPI(t)
	s := newAppStore(f)
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories(),
		Steps: []resource.TestStep{
			{Config: appSettingsConfig(f, `  name = "x"`)},
			{PreConfig: func() { s.mu.Lock(); s.exists = false; s.mu.Unlock() },
				Config: appSettingsConfig(f, `  name = "x"`), PlanOnly: true, ExpectNonEmptyPlan: true},
		},
	})
}

func appInstanceConfig(f *fakeAPI, tier string) string {
	return f.providerConfig() + `
resource "mtncloud_app_instance" "w" {
  app_id      = "9901"
  instance_id = "9750"
  tier        = "` + tier + `"
}
`
}

// Create adds the instance; a tier change moves it; removal outside Terraform plans it again;
// destroy takes it out and leaves the order's own instance alone.
func TestAppInstance_Lifecycle(t *testing.T) {
	f := newFakeAPI(t)
	s := newAppStore(f)
	tierOf := func(want string) resource.TestCheckFunc {
		return func(*terraform.State) error {
			s.mu.Lock()
			defer s.mu.Unlock()
			if got := s.tiers[9750]; got != want {
				t.Errorf("instance 9750 in tier %q, want %q", got, want)
			}
			return nil
		}
	}
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories(),
		CheckDestroy: func(*terraform.State) error {
			s.mu.Lock()
			defer s.mu.Unlock()
			if _, in := s.tiers[9750]; in {
				t.Error("destroy left instance 9750 in the app")
			}
			if _, in := s.tiers[9701]; !in || slices.Contains(s.removed, 9701) {
				t.Error("destroy touched the order's own instance 9701")
			}
			return nil
		},
		Steps: []resource.TestStep{
			{Config: appInstanceConfig(f, "Worker"), Check: tierOf("Worker")},
			{Config: appInstanceConfig(f, "Worker"), ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()}}},
			{Config: appInstanceConfig(f, "App"), ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
				plancheck.ExpectResourceAction("mtncloud_app_instance.w", plancheck.ResourceActionDestroyBeforeCreate)}}, Check: tierOf("App")},
			{PreConfig: func() { s.mu.Lock(); delete(s.tiers, 9750); s.mu.Unlock() },
				Config: appInstanceConfig(f, "App"), Check: tierOf("App")},
			{Config: appInstanceConfig(f, "App"), ResourceName: "mtncloud_app_instance.w", ImportState: true,
				ImportStateId: "9901/9750", ImportStateVerify: true},
			{Config: appInstanceConfig(f, "App"), ResourceName: "mtncloud_app_instance.w", ImportState: true,
				ImportStateId: "9750", ExpectError: regexp.MustCompile(`isn't <app_id>/<instance_id>`)},
		},
	})
}

func appConfig(f *fakeAPI, body string) string {
	return f.providerConfig() + "resource \"mtncloud_app\" \"shop\" {\n  name  = \"tf-acc-app-shop\"\n  group = \"MTNNG_CLOUD_AZ_1\"\n" + body + "\n}\n"
}

// A blank app: made with blueprint "existing", changed in place, a member joins it, a name already
// in use is refused, and destroy deletes the app but keeps its instances (B-017).
func TestApp_Lifecycle(t *testing.T) {
	f := newFakeAPI(t)
	s := newAppStore(f)
	attr := func(a string, v knownvalue.Check) statecheck.StateCheck {
		return statecheck.ExpectKnownValue("mtncloud_app.shop", tfjsonpath.New(a), v)
	}
	empty := resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()}}
	member := `
resource "mtncloud_app_instance" "w" {
  app_id      = mtncloud_app.shop.id
  instance_id = "9750"
  tier        = "Web"
}
`
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories(),
		CheckDestroy: func(*terraform.State) error {
			s.mu.Lock()
			defer s.mu.Unlock()
			if len(s.made) != 0 || len(s.deletes) == 0 {
				t.Errorf("%d apps left after destroy, %d deleted", len(s.made), len(s.deletes))
			}
			for _, d := range s.deletes {
				if !strings.HasSuffix(d, "removeInstances=off") {
					t.Errorf("app deleted with %q: destroy must keep its instances", d)
				}
			}
			return nil
		},
		Steps: []resource.TestStep{
			{Config: appConfig(f, `  labels = ["shop"]
  environment = "Production"`), ConfigStateChecks: []statecheck.StateCheck{
				attr("id", knownvalue.StringExact("9951")),
				attr("environment", knownvalue.StringExact("Production")),
				attr("description", knownvalue.StringExact("")),
			}, Check: func(*terraform.State) error {
				s.mu.Lock()
				defer s.mu.Unlock()
				p := s.posts[0]
				if p["blueprintId"] != "existing" || p["environment"] != "production" || p["group"].(map[string]any)["id"] == nil {
					t.Errorf("create sent %v: want a blank app with the environment's code and the group", p)
				}
				return nil
			}},
			{Config: appConfig(f, `  labels = ["shop"]
  environment = "Production"`), ConfigPlanChecks: empty},
			{Config: appConfig(f, `  labels = ["shop", "web"]
  description = "Shop front"
  environment = "Production"`) + member, ConfigStateChecks: []statecheck.StateCheck{
				attr("labels", knownvalue.SetSizeExact(2)),
				attr("description", knownvalue.StringExact("Shop front")),
			}, ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
				plancheck.ExpectResourceAction("mtncloud_app.shop", plancheck.ResourceActionUpdate)}}},
			{Config: appConfig(f, `  labels = ["shop", "web"]
  description = "Shop front"
  environment = "Production"`) + member, ConfigStateChecks: []statecheck.StateCheck{
				attr("instance_ids", knownvalue.SetExact([]knownvalue.Check{knownvalue.StringExact("9750")})),
			}},
			{Config: appConfig(f, `  labels = ["shop", "web"]
  description = "Shop front"
  environment = "Production"`) + member, ResourceName: "mtncloud_app.shop", ImportState: true, ImportStateId: "9951", ImportStateVerify: true},
			// The name of an app that already exists is refused, not taken over.
			{Config: appConfig(f, `  labels = ["shop", "web"]
  description = "Shop front"
  environment = "Production"`) + member + `
resource "mtncloud_app" "dup" {
  name  = "ab-42-MTN-CLOUD-MYSQL"
  group = "MTNNG_CLOUD_AZ_1"
}
`, ExpectError: regexp.MustCompile(`(?s)App name in use.*terraform import`)},
			// Deleted outside Terraform: the plan makes it again.
			{PreConfig: func() { s.mu.Lock(); delete(s.made, 9951); s.mu.Unlock() },
				Config: appConfig(f, ""), PlanOnly: true, ExpectNonEmptyPlan: true},
			// Made again, so the final destroy deletes one through the API.
			{Config: appConfig(f, ""), ConfigStateChecks: []statecheck.StateCheck{attr("id", knownvalue.StringExact("9952"))}},
		},
	})
}

// group left out takes the provider's; an app found in another group (moved, or imported) plans a
// replace rather than an update that can't apply.
func TestApp_DefaultGroup(t *testing.T) {
	f := newFakeAPI(t)
	s := newAppStore(f)
	cfg := strings.Replace(f.providerConfig(), `access_token = "test-token"`, "access_token = \"test-token\"\n  group        = \"MTNNG_CLOUD_AZ_1\"", 1) +
		"resource \"mtncloud_app\" \"shop\" {\n  name = \"tf-acc-app-shop\"\n}\n"
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories(),
		Steps: []resource.TestStep{
			{Config: cfg, ConfigStateChecks: []statecheck.StateCheck{
				statecheck.ExpectKnownValue("mtncloud_app.shop", tfjsonpath.New("group"), knownvalue.StringExact("MTNNG_CLOUD_AZ_1"))}},
			{Config: cfg, ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()}}},
			{PreConfig: func() {
				s.mu.Lock()
				for _, a := range s.made {
					a["group"] = map[string]any{"name": "OTHER_GROUP"}
				}
				s.mu.Unlock()
			}, Config: cfg, PlanOnly: true, ExpectNonEmptyPlan: true, ConfigPlanChecks: resource.ConfigPlanChecks{
				PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectResourceAction("mtncloud_app.shop", plancheck.ResourceActionReplace)}}},
		},
	})
}

// environment = "" clears an ordered app's environment.
func TestAppSettings_ClearEnvironment(t *testing.T) {
	f := newFakeAPI(t)
	s := newAppStore(f)
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories(),
		Steps: []resource.TestStep{
			{Config: appSettingsConfig(f, `  environment = ""`), Check: func(*terraform.State) error {
				s.mu.Lock()
				defer s.mu.Unlock()
				if len(s.puts) != 1 {
					t.Fatalf("%d updates, want 1", len(s.puts))
				}
				if env, ok := s.puts[0]["environment"]; !ok || env != "" {
					t.Errorf("update sent environment %v (set %v), want it cleared", env, ok)
				}
				return nil
			}},
			{Config: appSettingsConfig(f, `  environment = ""`), ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()}}},
		},
	})
}
