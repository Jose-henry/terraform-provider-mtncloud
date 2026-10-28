package provider

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
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

// taskAPI is an in-memory /api/tasks with the option lists the task types name things from. It
// stores what it is sent and returns it, as the platform does (shapes from `tasks add -d`).
type taskAPI struct {
	mu     sync.Mutex
	nextID int64
	tasks  map[int64]map[string]any
	last   map[string]any // the last create or update body's "task"
	// defaultMethod, when set, is what the platform fills in for an HTTP task's missing method.
	defaultMethod string
}

func newTaskAPI(t *testing.T) (*taskAPI, string) {
	t.Helper()
	clearDefaults(t)
	a := &taskAPI{nextID: 700, tasks: map[int64]map[string]any{}}
	options := map[string]string{
		"aiAgents":           `[{"name":"ops-assistant","value":11},{"name":"other","value":12}]`,
		"operationWorkflows": `[{"name":"patch-with-approval","value":21},{"name":"patch-now","value":22}]`,
		"puppet":             `[{"name":"puppet-prod","value":31}]`,
		"credentials":        `[{"name":"deploy-hook","value":41}]`,
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if src, ok := strings.CutPrefix(r.URL.Path, "/api/options/"); ok {
			_, _ = w.Write([]byte(`{"data":` + options[src] + `}`))
			return
		}
		a.mu.Lock()
		defer a.mu.Unlock()
		reply := func(v any) { _ = json.NewEncoder(w).Encode(v) }
		idStr, one := strings.CutPrefix(r.URL.Path, "/api/tasks/")
		id, _ := strconv.ParseInt(idStr, 10, 64)
		switch {
		case r.URL.Path == "/api/tasks" && r.Method == http.MethodGet:
			var out []map[string]any
			for _, task := range a.tasks {
				if name := r.URL.Query().Get("name"); name == "" || task["name"] == name {
					out = append(out, task)
				}
			}
			reply(map[string]any{"tasks": out, "meta": map[string]any{"total": len(out)}})
		case r.URL.Path == "/api/tasks" && r.Method == http.MethodPost:
			var body map[string]map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			a.nextID++
			task := body["task"]
			if opts, _ := task["taskOptions"].(map[string]any); opts != nil && a.defaultMethod != "" && opts["webMethod"] == nil {
				opts["webMethod"] = a.defaultMethod
			}
			task["id"] = a.nextID
			a.tasks[a.nextID], a.last = task, task
			reply(map[string]any{"success": true, "task": task})
		case one && a.tasks[id] == nil:
			w.WriteHeader(http.StatusNotFound)
		case one && r.Method == http.MethodGet:
			reply(map[string]any{"task": a.tasks[id]})
		case one && r.Method == http.MethodPut:
			var body map[string]map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			// Worst case: the platform merges the options sent into the stored ones.
			task := body["task"]
			task["id"] = id
			for _, k := range []string{"taskOptions", "config"} {
				old, _ := a.tasks[id][k].(map[string]any)
				sent, _ := task[k].(map[string]any)
				merged := map[string]any{}
				for kk, v := range old {
					merged[kk] = v
				}
				for kk, v := range sent {
					merged[kk] = v
				}
				if len(merged) > 0 {
					task[k] = merged
				}
			}
			a.tasks[id], a.last = task, task
			reply(map[string]any{"success": true, "task": task})
		case one && r.Method == http.MethodDelete:
			delete(a.tasks, id)
			reply(map[string]any{"success": true})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return a, `
provider "mtncloud" {
  url          = "` + srv.URL + `"
  access_token = "test-token"
}
`
}

func (a *taskAPI) lastBody() map[string]any {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.last
}

// Every task type's example: create, an empty plan, import.
func TestTaskTypes_Examples(t *testing.T) {
	for typ, cfg := range map[string]string{
		"ai_task":                            `ai_agent = "ops-assistant"` + "\n" + `prompt = "hi"`,
		"conditional_workflow_task":          `if_workflow = "patch-with-approval"` + "\n" + `else_workflow = "patch-now"`,
		"dynamic_wallet_collection_task":     ``,
		"http_task":                          `url = "https://hooks.example.com/x"`,
		"powerscale_storage_collection_task": `dry_run = true`,
		"puppet_agent_install_task":          `puppet_master = "puppet-prod"` + "\n" + `puppet_version = "7"`,
		"reverse_text_task":                  `text = "hello"`,
	} {
		t.Run(typ, func(t *testing.T) {
			_, provider := newTaskAPI(t)
			addr := "mtncloud_" + typ + ".x"
			config := provider + "resource \"mtncloud_" + typ + "\" \"x\" {\n  name = \"tf-" + typ + "\"\n" + cfg + "\n}\n"
			resource.UnitTest(t, resource.TestCase{
				ProtoV6ProviderFactories: factories(),
				Steps: []resource.TestStep{
					{Config: config},
					{Config: config, ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()}}},
					{Config: config, ResourceName: addr, ImportState: true, ImportStateVerify: true},
				},
			})
		})
	}
}

func TestHTTPTask(t *testing.T) {
	a, provider := newTaskAPI(t)
	cfg := func(extra string) string {
		return provider + `
resource "mtncloud_http_task" "notify" {
  name    = "notify"
  url     = "https://hooks.example.com/deploy"
  method  = "POST"
  headers = { "Content-Type" = "application/json" }
` + extra + `
}
`
	}
	check := func(attr string, v knownvalue.Check) statecheck.StateCheck {
		return statecheck.ExpectKnownValue("mtncloud_http_task.notify", tfjsonpath.New(attr), v)
	}
	sent := func(fn func(task, opts map[string]any)) resource.TestCheckFunc {
		return func(*terraform.State) error {
			task := a.lastBody()
			opts, _ := task["taskOptions"].(map[string]any)
			fn(task, opts)
			return nil
		}
	}
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories(),
		Steps: []resource.TestStep{
			{
				Config: cfg(`  password = "s3cret"
  ignore_ssl = true
  result_type = "json"`),
				Check: sent(func(task, opts map[string]any) {
					if tt, _ := task["taskType"].(map[string]any); tt["code"] != "httpTask" || task["executeTarget"] != "local" {
						t.Errorf("type/target: %v %v", task["taskType"], task["executeTarget"])
					}
					if opts["webUrl"] != "https://hooks.example.com/deploy" || opts["webPassword"] != "s3cret" || opts["ignoreSSL"] != "on" {
						t.Errorf("options: %v", opts)
					}
					if opts["httpHeaders"] != `{"Content-Type":"application/json"}` {
						t.Errorf("headers: %v", opts["httpHeaders"])
					}
					if c, _ := task["credential"].(map[string]any); c["type"] != "local" {
						t.Errorf("credential: %v", task["credential"])
					}
				}),
				ConfigStateChecks: []statecheck.StateCheck{
					check("headers", knownvalue.MapExact(map[string]knownvalue.Check{"Content-Type": knownvalue.StringExact("application/json")})),
					check("ignore_ssl", knownvalue.Bool(true)),
					check("retry_count", knownvalue.Int64Exact(5)),
					check("visibility", knownvalue.StringExact("private")),
				},
			},
			{
				// A stored credential, by name, sent by ID; read back by the name given.
				Config: cfg(`  credential = "Deploy-Hook"`),
				Check: sent(func(task, opts map[string]any) {
					if c, _ := task["credential"].(map[string]any); c["id"] != float64(41) {
						t.Errorf("credential: %v", task["credential"])
					}
					// Sent as null, so even a platform that merges options clears it.
					if v, ok := opts["webPassword"]; !ok || v != nil || opts["ignoreSSL"] != nil {
						t.Errorf("a removed password or ignore_ssl was kept: %v", opts)
					}
				}),
				ConfigStateChecks: []statecheck.StateCheck{check("credential", knownvalue.StringExact("Deploy-Hook")), check("ignore_ssl", knownvalue.Bool(false))},
			},
			{
				Config:           cfg(`  credential = "Deploy-Hook"`),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()}},
			},
			{Config: cfg(`  credential = "nope"`), PlanOnly: true, ExpectError: regexp.MustCompile(`(?s)Unknown credential.*deploy-hook`)},
			{
				// Deleted outside Terraform: planned again (rule 8).
				PreConfig: func() {
					a.mu.Lock()
					clear(a.tasks)
					a.mu.Unlock()
				},
				Config:             cfg(`  credential = "Deploy-Hook"`),
				PlanOnly:           true,
				ExpectNonEmptyPlan: true,
			},
			{Config: cfg(`  credential = "Deploy-Hook"`)},
		},
	})
}

func TestConditionalWorkflowTask(t *testing.T) {
	a, provider := newTaskAPI(t)
	cfg := func(ifWF, extra string) string {
		return provider + `
resource "mtncloud_conditional_workflow_task" "c" {
  name        = "by-env"
  condition   = "return true"
  if_workflow = "` + ifWF + `"
` + extra + `
}
`
	}
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories(),
		Steps: []resource.TestStep{
			{Config: cfg("nope", ""), ExpectError: regexp.MustCompile(`(?s)Unknown operational workflow.*patch-now`)},
			{
				Config: cfg("Patch-With-Approval", `  else_workflow = "patch-now"`),
				Check: func(*terraform.State) error {
					opts, _ := a.lastBody()["taskOptions"].(map[string]any)
					if opts["ifOperationalWorkflowId"] != "21" || opts["ifOperationalWorkflowName"] != "patch-with-approval" ||
						opts["elseOperationalWorkflowId"] != "22" || opts["conditionalScript"] != "return true" {
						t.Errorf("options: %v", opts)
					}
					return nil
				},
			},
			{
				// Removing the else workflow updates in place and stops sending it.
				Config: cfg("Patch-With-Approval", ""),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue("mtncloud_conditional_workflow_task.c", tfjsonpath.New("else_workflow"), knownvalue.Null()),
					statecheck.ExpectKnownValue("mtncloud_conditional_workflow_task.c", tfjsonpath.New("if_workflow"), knownvalue.StringExact("Patch-With-Approval")),
				},
			},
		},
	})
}

// A plugin option (field context "config") goes in "config", not "taskOptions"; the target can be chosen.
func TestReverseTextTask_ConfigOptions(t *testing.T) {
	a, provider := newTaskAPI(t)
	cfg := provider + `
resource "mtncloud_reverse_text_task" "r" {
  name           = "rev"
  text           = "hello"
  execute_target = "local"
}
`
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories(),
		Steps: []resource.TestStep{{
			Config: cfg,
			Check: func(*terraform.State) error {
				task := a.lastBody()
				conf, _ := task["config"].(map[string]any)
				if conf["reversibleText"] != "hello" || task["taskOptions"] != nil || task["executeTarget"] != "local" {
					t.Errorf("body: %v", task)
				}
				return nil
			},
		}},
	})
}

func TestTask_ExistingNameIsRefused(t *testing.T) {
	a, provider := newTaskAPI(t)
	a.tasks[5] = map[string]any{"id": 5, "name": "rev", "taskType": map[string]any{"code": "reverseTextTask"}}
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories(),
		Steps: []resource.TestStep{{
			Config:      provider + "resource \"mtncloud_reverse_text_task\" \"r\" {\n  name = \"rev\"\n  text = \"x\"\n}\n",
			ExpectError: regexp.MustCompile(`(?s)Task already exists.*terraform import <address> 5`),
		}},
	})
}

// Options the platform fills in, an empty label set, a credential with a user, and importing
// another type's task.
func TestTask_PlatformDefaultsLabelsAndType(t *testing.T) {
	a, provider := newTaskAPI(t)
	a.tasks[5] = map[string]any{"id": 5, "name": "h", "taskType": map[string]any{"code": "httpTask"}}
	a.defaultMethod = "GET"
	cfg := provider + `
resource "mtncloud_http_task" "h" {
  name   = "h2"
  url    = "https://x.example"
  labels = []
}
`
	other := provider + `
resource "mtncloud_reverse_text_task" "r" {
  name = "h"
  text = "x"
}
`
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories(),
		Steps: []resource.TestStep{
			{
				Config: cfg,
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue("mtncloud_http_task.h", tfjsonpath.New("method"), knownvalue.StringExact("GET")),
					statecheck.ExpectKnownValue("mtncloud_http_task.h", tfjsonpath.New("labels"), knownvalue.SetSizeExact(0)),
				},
			},
			{Config: cfg, ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()}}},
			{Config: other, ResourceName: "mtncloud_reverse_text_task.r", ImportState: true, ImportStateId: "5",
				ExpectError: regexp.MustCompile(`(?s)Wrong task type.*httpTask`)},
			{Config: cfg + `
resource "mtncloud_http_task" "c" {
  name       = "c"
  url        = "https://x.example"
  credential = "deploy-hook"
  user       = "me"
}
`, PlanOnly: true, ExpectError: regexp.MustCompile(`Invalid Attribute Combination`)},
			{Config: cfg},
		},
	})
}
