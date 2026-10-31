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

// orderStore fakes the catalog inventory: an ordered item reads once with no execution (the
// workflow hasn't started), then with the execution's result.
type orderStore struct {
	mu        sync.Mutex
	next      int64
	items     map[int64]map[string]any
	reads     map[int64]int
	orders    int
	lastOrder map[string]any
	result    string // the execution's final status; "" never reports one
	postFail  int    // the order POST places the order, then answers with this status
	// made, when set, runs once when an order's execution succeeds: what the workflow creates.
	made func(order map[string]any)
	sent map[int64]map[string]any // each order's item, as posted
	// apps maps an app ID to its instance IDs; deleting one (removeInstances=on) deletes them from
	// insts. deleted lists the inventory records and apps deleted, in order ("item 9801", "app 9901").
	apps    map[int64][]int64
	appName map[int64]string   // an app's name, "app-<id>" when not set
	appStep map[int64][]string // an app's status on each list, the last kept; "running" when not set
	failure string             // the failed task's output in every execution, when set
	insts   *instStore
	deleted []string
}

func newOrderFakes(t *testing.T) (*fakeAPI, *orderStore) {
	t.Helper()
	f := newFakeAPI(t)
	s := &orderStore{next: 9801, items: map[int64]map[string]any{}, reads: map[int64]int{}, result: "success"}
	// Another kind of order, to show a project can't be imported from it.
	s.items[9790] = map[string]any{"id": 9790, "type": map[string]any{"id": 13, "name": "Create MOS", "type": "workflow"}, "status": "ORDERED"}
	// The order of the synced project "web-prod-Acme", placed in the console.
	s.items[9795] = map[string]any{"id": 9795, "type": map[string]any{"id": 10, "name": "Create Project", "type": "workflow"},
		"status": "ORDERED", "execution": map[string]any{"id": 9796, "status": "success"}}
	f.order = s
	return f, s
}

func (s *orderStore) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	reply := func(status int, v any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(v)
	}
	if r.Method == http.MethodPost && r.URL.Path == "/api/catalog/orders" {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		s.lastOrder, s.orders = body, s.orders+1
		id := s.next
		s.next++
		name := "Create Project"
		if items, _ := body["order"].(map[string]any)["items"].([]any); len(items) == 1 {
			posted, _ := items[0].(map[string]any)
			name, _ = posted["type"].(map[string]any)["name"].(string)
			if s.sent == nil {
				s.sent = map[int64]map[string]any{}
			}
			s.sent[id] = posted
		}
		item := map[string]any{"id": id, "type": map[string]any{"id": 10, "name": name, "type": "workflow"},
			"status": "ORDERED", "statusMessage": nil, "execution": nil}
		s.items[id] = item
		if s.postFail != 0 {
			reply(s.postFail, map[string]any{"success": false, "msg": "upstream timed out"})
			return
		}
		reply(http.StatusOK, map[string]any{"success": true, "order": map[string]any{"id": id - 1, "items": []any{item}}})
		return
	}
	if strings.HasPrefix(r.URL.Path, "/api/apps") {
		s.serveApps(r, reply)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/api/job-executions/") {
		ev := []any{map[string]any{"status": "complete", "output": "Validating"}}
		if s.failure != "" {
			ev = append(ev, map[string]any{"status": "failed", "output": s.failure})
		}
		reply(http.StatusOK, map[string]any{"jobExecution": map[string]any{"process": map[string]any{"events": ev}}})
		return
	}
	id, _ := strconv.ParseInt(strings.TrimPrefix(r.URL.Path, "/api/catalog/items/"), 10, 64)
	if r.Method == http.MethodDelete {
		if _, ok := s.items[id]; !ok {
			reply(http.StatusNotFound, map[string]any{"success": false, "msg": "Item not found"})
			return
		}
		delete(s.items, id)
		s.deleted = append(s.deleted, fmt.Sprint("item ", id))
		reply(http.StatusOK, map[string]any{"success": true})
		return
	}
	item, ok := s.items[id]
	if r.Method != http.MethodGet || !ok {
		reply(http.StatusNotFound, map[string]any{"success": false, "msg": "Item not found"})
		return
	}
	if s.reads[id]++; s.reads[id] > 1 && s.result != "" && item["execution"] == nil && id != 9790 {
		item["execution"] = map[string]any{"id": id + 10, "jobId": id + 11, "status": s.result}
		if s.result == "error" {
			item["statusMessage"] = "Resource pool create returned 400"
		}
		if s.result == "success" && s.made != nil {
			s.made(s.sent[id])
		}
	}
	reply(http.StatusOK, map[string]any{"item": item})
}

// serveApps lists the apps, and deletes one with its instances.
func (s *orderStore) serveApps(r *http.Request, reply func(int, any)) {
	if r.Method == http.MethodGet && r.URL.Path == "/api/apps" {
		list := []any{}
		for id, held := range s.apps {
			var insts []any
			for _, i := range held {
				insts = append(insts, map[string]any{"id": i, "name": fmt.Sprint("node-", i)})
			}
			name, status := fmt.Sprint("app-", id), "running"
			if n, ok := s.appName[id]; ok {
				name = n
			}
			if st := s.appStep[id]; len(st) > 0 {
				status = st[0]
				if len(st) > 1 {
					s.appStep[id] = st[1:]
				}
			}
			list = append(list, map[string]any{"id": id, "name": name, "status": status, "instances": insts})
		}
		reply(http.StatusOK, map[string]any{"apps": list, "meta": map[string]any{"total": len(list)}})
		return
	}
	id, _ := strconv.ParseInt(strings.TrimPrefix(r.URL.Path, "/api/apps/"), 10, 64)
	held, ok := s.apps[id]
	if r.Method != http.MethodDelete || !ok {
		reply(http.StatusNotFound, map[string]any{"success": false, "msg": "App not found"})
		return
	}
	if r.URL.Query().Get("removeInstances") == "on" && s.insts != nil {
		s.insts.mu.Lock()
		for _, i := range held {
			delete(s.insts.insts, i)
		}
		s.insts.mu.Unlock()
	}
	delete(s.apps, id)
	s.deleted = append(s.deleted, fmt.Sprint("app ", id))
	reply(http.StatusOK, map[string]any{"success": true})
}

func projectConfig(f *fakeAPI, body string) string {
	return f.providerConfig() + `
resource "mtncloud_project" "web" {
` + body + `
}
`
}

func TestProject_Lifecycle(t *testing.T) {
	f, s := newOrderFakes(t)
	cfg := func(name, desc string) string {
		return projectConfig(f, `  name        = "`+name+`"
  description = "`+desc+`"`)
	}
	attr := func(p string, v knownvalue.Check) statecheck.StateCheck {
		return statecheck.ExpectKnownValue("mtncloud_project.web", tfjsonpath.New(p), v)
	}
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories(),
		Steps: []resource.TestStep{
			{
				Config: cfg("web", "Web tier"),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectKnownValue("mtncloud_project.web", tfjsonpath.New("full_name"), knownvalue.StringExact("web-Acme")),
				}},
				ConfigStateChecks: []statecheck.StateCheck{
					attr("id", knownvalue.StringExact("9801")),
					attr("full_name", knownvalue.StringExact("web-Acme")),
					attr("status", knownvalue.StringExact("success")),
				},
			},
			{
				PreConfig: func() {
					s.mu.Lock()
					defer s.mu.Unlock()
					item := s.lastOrder["order"].(map[string]any)["items"].([]any)[0].(map[string]any)
					conf := item["config"].(map[string]any)
					if item["context"] != "appliance" || item["type"].(map[string]any)["name"] != "Create Project" ||
						conf["projectName"] != "web" || conf["projectDescription"] != "Web tier" || conf["cloud"] == "" || conf["groupName"] == "" {
						t.Errorf("order body %v", s.lastOrder)
					}
				},
				Config:           cfg("web", "Web tier"),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()}},
			},
			{Config: cfg("api", "Web tier"), PlanOnly: true, ExpectError: regexp.MustCompile(`(?s)A project can't be changed.*Set it back to "web"`)},
			{Config: cfg("web", "Other"), PlanOnly: true, ExpectError: regexp.MustCompile(`A project can't be changed`)},
			{
				ResourceName:            "mtncloud_project.web",
				ImportState:             true,
				ImportStateVerify:       true,
				ImportStateVerifyIgnore: []string{"name", "description", "full_name", "timeouts"},
			},
			{ResourceName: "mtncloud_project.web", ImportState: true, ImportStateId: "9790", ExpectError: regexp.MustCompile(`Not a project order`)},
			{ResourceName: "mtncloud_project.web", ImportState: true, ImportStateId: "web", ExpectError: regexp.MustCompile(`catalog order ID`)},
			{Config: f.providerConfig(), ExpectError: regexp.MustCompile(`(?s)can't be deleted from Terraform.*removed block`)},
			{
				// The documented way out: forget it, keep the project.
				Config: f.providerConfig() + `
removed {
  from = mtncloud_project.web
  lifecycle {
    destroy = false
  }
}
`,
			},
		},
	})
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.orders != 1 {
		t.Errorf("%d orders, want 1", s.orders)
	}
}

func TestProject_Errors(t *testing.T) {
	f, s := newOrderFakes(t)
	set := func(result string) func() {
		return func() { s.mu.Lock(); defer s.mu.Unlock(); s.result = result }
	}
	forget := f.providerConfig() + `
removed {
  from = mtncloud_project.web
  lifecycle {
    destroy = false
  }
}
`
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories(),
		Steps: []resource.TestStep{
			{Config: projectConfig(f, `  name = "Web"`), PlanOnly: true, ExpectError: regexp.MustCompile(`must begin with a lowercase letter`)},
			// "web-prod-Acme" is in the project list already.
			{Config: projectConfig(f, `  name = "web-prod"`), PlanOnly: true, ExpectError: regexp.MustCompile(`(?s)Project web-prod-Acme already exists`)},
			{
				// A failed workflow made nothing, so nothing is kept and the next apply orders again.
				PreConfig:   set("error"),
				Config:      projectConfig(f, `  name = "web"`),
				ExpectError: regexp.MustCompile(`(?s)Project order failed.*"error": Resource pool\s+create returned 400`),
			},
			{
				PreConfig:        set("success"),
				Config:           projectConfig(f, `  name = "web"`),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction("mtncloud_project.web", plancheck.ResourceActionCreate)}},
			},
			{Config: forget},
			{
				// No result within the timeout: kept (a warning), never ordered twice.
				PreConfig: set(""),
				Config: projectConfig(f, `  name = "api"
  timeouts {
    create = "2s"
  }`),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue("mtncloud_project.web", tfjsonpath.New("status"), knownvalue.StringExact("ordered")),
				},
			},
			{Config: forget},
		},
	})
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.orders != 3 {
		t.Errorf("%d orders, want 3 (failed, retried, timed out)", s.orders)
	}
}

func TestProject_ImportAndLateResults(t *testing.T) {
	f, s := newOrderFakes(t)
	lock := func(fn func()) func() { return func() { s.mu.Lock(); defer s.mu.Unlock(); fn() } }
	forget := f.providerConfig() + `
removed {
  from = mtncloud_project.web
  lifecycle {
    destroy = false
  }
}
`
	action := func(a plancheck.ResourceActionType) resource.ConfigPlanChecks {
		return resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction("mtncloud_project.web", a)}}
	}
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories(),
		Steps: []resource.TestStep{
			{
				// Import a project ordered in the console; the configuration gives its name.
				Config:             projectConfig(f, `  name = "web-prod"`),
				ResourceName:       "mtncloud_project.web",
				ImportState:        true,
				ImportStateId:      "9795",
				ImportStatePersist: true,
			},
			{
				// The first apply records the name and sends nothing; then nothing changes.
				Config:           projectConfig(f, `  name = "web-prod"`),
				ConfigPlanChecks: action(plancheck.ResourceActionUpdate),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue("mtncloud_project.web", tfjsonpath.New("full_name"), knownvalue.StringExact("web-prod-Acme")),
				},
			},
			{
				Config:           projectConfig(f, `  name = "web-prod"`),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()}},
			},
			{
				Config:      projectConfig(f, `  name = "web-prod"`+"\n"+`  description = "x"`),
				PlanOnly:    true,
				ExpectError: regexp.MustCompile(`Remove\s+description: the project was ordered without one`),
			},
			{Config: forget},
			{
				// Imported under a name that isn't the project's.
				Config:             projectConfig(f, `  name = "api"`),
				ResourceName:       "mtncloud_project.web",
				ImportState:        true,
				ImportStateId:      "9795",
				ImportStatePersist: true,
			},
			{Config: projectConfig(f, `  name = "api"`), PlanOnly: true, ExpectError: regexp.MustCompile(`(?s)No such project.*api-Acme`)},
			{Config: forget},
			{
				// A gateway error after the order was placed: say so, keep nothing, order once.
				PreConfig:   lock(func() { s.postFail = http.StatusBadGateway }),
				Config:      projectConfig(f, `  name = "web"`),
				ExpectError: regexp.MustCompile(`(?s)order may have been placed.*Catalog\s+>\s+Inventory`),
			},
			{
				// No result within the timeout, then the workflow fails: the refresh drops it, so the next
				// apply orders again.
				PreConfig: lock(func() { s.postFail, s.result = 0, "" }),
				Config: projectConfig(f, `  name = "web"
  timeouts {
    create = "2s"
  }`),
			},
			{
				PreConfig:          lock(func() { s.result = "error" }),
				RefreshState:       true,
				ExpectNonEmptyPlan: true, // dropped from state, so the plan orders it again
			},
		},
	})
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.orders != 2 {
		t.Errorf("%d orders, want 2 (the 502 and the timed-out one)", s.orders)
	}
}
