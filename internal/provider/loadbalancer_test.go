package provider

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/knownvalue"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/statecheck"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"
)

// lbTask is the load balancer operations task: it answers from the recorded job executions in
// client/testdata/lb for network 114, and refuses other networks and unknown load balancers the way
// the plugin words it.
type lbTask struct {
	mu      sync.Mutex
	actions map[int64]map[string]any // job execution ID -> the customOptions it ran with
	next    int64
	reads   int // "read" runs
}

func newLBTask() *lbTask { return &lbTask{actions: map[int64]map[string]any{}, next: 31000} }

func (s *lbTask) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/api/tasks":
		_, _ = w.Write([]byte(`{"tasks":[{"id":259,"code":"iportroutek123456"},{"id":450,"code":"lboperate.terraform"}],"meta":{"total":2}}`))
	case r.Method == http.MethodPost && r.URL.Path == "/api/tasks/450/execute":
		var body struct {
			Job struct {
				CustomOptions map[string]any `json:"customOptions"`
			} `json:"job"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		s.mu.Lock()
		s.next++
		id := s.next
		s.actions[id] = body.Job.CustomOptions
		if body.Job.CustomOptions["action"] == "read" {
			s.reads++
		}
		s.mu.Unlock()
		_, _ = fmt.Fprintf(w, `{"success":true,"jobExecution":{"id":%d}}`, id)
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/api/job-executions/"):
		var id int64
		_, _ = fmt.Sscan(strings.TrimPrefix(r.URL.Path, "/api/job-executions/"), &id)
		s.mu.Lock()
		opts := s.actions[id]
		s.mu.Unlock()
		_, _ = w.Write(s.answer(opts))
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func (s *lbTask) answer(opts map[string]any) []byte {
	refuse := func(msg string) []byte {
		env, _ := json.Marshal(map[string]any{"success": false, "action": opts["action"], "error": msg})
		b, _ := json.Marshal(map[string]any{"jobExecution": map[string]any{"status": "error", "process": map[string]any{
			"events": []any{map[string]any{"processType": map[string]any{"code": "executeTask"}, "output": string(env)}}}}})
		return b
	}
	if opts["networkId"] != "114" {
		return refuse(fmt.Sprintf("Network %v is not available to your tenant.", opts["networkId"]))
	}
	file := map[any]string{"list": "list", "read": "read", "fip-options": "fip"}[opts["action"]]
	if opts["action"] == "read" && opts["name"] != nil && opts["name"] != "My Load Balancer" {
		// The plugin's own wording (LbOperationsTaskService, read by name).
		return refuse(fmt.Sprintf("No load balancer named '%v' on network 114.", opts["name"]))
	}
	if opts["action"] == "read" && opts["lbId"] == "00000000-0000-4000-8000-0000000000ee" {
		return refuse("OpenStack reported load balancer 00000000-0000-4000-8000-0000000000ee in ERROR.") // mtncloud:allow-cloud-name (the plugin's wording, scrubbed by the client)
	}
	b, err := os.ReadFile(filepath.Join("..", "client", "testdata", "lb", file+".json"))
	if err != nil {
		return refuse("fake: no fixture for " + fmt.Sprint(opts["action"]))
	}
	return b
}

const (
	lbID      = "00000000-0000-4000-8000-000000000006"
	lbPoolID  = "00000000-0000-4000-8000-000000000016"
	lbMonitor = "00000000-0000-4000-8000-000000000020"
)

func TestLoadBalancerDataSources(t *testing.T) {
	f := newFakeAPI(t)
	task := newLBTask()
	f.lb = task
	cfg := f.providerConfig() + `
data "mtncloud_load_balancers" "all" {
  network_id = 114
}
data "mtncloud_load_balancers" "active" {
  network_id = 114
  status     = "active"
}
data "mtncloud_load_balancer" "web" {
  network_id = 114
  name       = "My Load Balancer"
}
data "mtncloud_load_balancer_listeners" "web" {
  network_id         = 114
  load_balancer_name = "My Load Balancer"
}
data "mtncloud_load_balancer_listener" "https" {
  network_id         = 114
  load_balancer_name = "My Load Balancer"
  name               = "TERMINATED_HTTPS1"
}
data "mtncloud_load_balancer_pools" "web" {
  network_id         = 114
  load_balancer_name = "My Load Balancer"
}
data "mtncloud_load_balancer_pool" "web" {
  network_id         = 114
  load_balancer_name = "My Load Balancer"
  id                 = "` + lbPoolID + `"
}
data "mtncloud_load_balancer_members" "web" {
  network_id         = 114
  load_balancer_name = "My Load Balancer"
  pool_name          = "HTTP-Pool"
}
data "mtncloud_load_balancer_health_monitors" "web" {
  network_id         = 114
  load_balancer_name = "My Load Balancer"
}
data "mtncloud_load_balancer_health_monitor" "web" {
  network_id         = 114
  load_balancer_name = "My Load Balancer"
  name               = "HTTP-HealthMonitor"
}
data "mtncloud_load_balancer_floating_ip_pools" "net" {
  network_id = 114
}
`
	str := knownvalue.StringExact
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories(),
		Steps: []resource.TestStep{
			{
				Config: cfg,
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue("data.mtncloud_load_balancers.all", tfjsonpath.New("can_manage"), knownvalue.Bool(true)),
					statecheck.ExpectKnownValue("data.mtncloud_load_balancers.all", tfjsonpath.New("load_balancers").AtSliceIndex(0).AtMapKey("name"), str("My Load Balancer")),
					statecheck.ExpectKnownValue("data.mtncloud_load_balancers.all", tfjsonpath.New("load_balancers").AtSliceIndex(0).AtMapKey("floating_ip"), str("203.0.113.2")),
					statecheck.ExpectKnownValue("data.mtncloud_load_balancers.all", tfjsonpath.New("load_balancers").AtSliceIndex(1).AtMapKey("floating_ip"), str("")),

					statecheck.ExpectKnownValue("data.mtncloud_load_balancer.web", tfjsonpath.New("id"), str(lbID)),
					statecheck.ExpectKnownValue("data.mtncloud_load_balancer.web", tfjsonpath.New("vip_address"), str("10.20.0.1")),
					statecheck.ExpectKnownValue("data.mtncloud_load_balancer.web", tfjsonpath.New("listeners"), knownvalue.ListSizeExact(2)),
					statecheck.ExpectKnownValue("data.mtncloud_load_balancer.web", tfjsonpath.New("pools").AtSliceIndex(0).AtMapKey("members"), knownvalue.ListSizeExact(3)),
					statecheck.ExpectKnownValue("data.mtncloud_load_balancer.web",
						tfjsonpath.New("pools").AtSliceIndex(0).AtMapKey("health_monitor").AtMapKey("type"), str("HTTP")),

					// Listeners are sorted by name, whatever order the platform sends.
					statecheck.ExpectKnownValue("data.mtncloud_load_balancer_listeners.web", tfjsonpath.New("listeners").AtSliceIndex(0).AtMapKey("name"), str("HTTP-Listener")),
					statecheck.ExpectKnownValue("data.mtncloud_load_balancer_listeners.web", tfjsonpath.New("load_balancer_id"), str(lbID)),
					statecheck.ExpectKnownValue("data.mtncloud_load_balancer_listener.https", tfjsonpath.New("port"), knownvalue.Int64Exact(443)),
					statecheck.ExpectKnownValue("data.mtncloud_load_balancer_listener.https", tfjsonpath.New("protocol"), str("TERMINATED_HTTPS")),
					statecheck.ExpectKnownValue("data.mtncloud_load_balancer_pools.web", tfjsonpath.New("pools").AtSliceIndex(0).AtMapKey("listener_ids"), knownvalue.ListSizeExact(2)),
					statecheck.ExpectKnownValue("data.mtncloud_load_balancer_pool.web", tfjsonpath.New("name"), str("HTTP-Pool")),
					statecheck.ExpectKnownValue("data.mtncloud_load_balancer_pool.web", tfjsonpath.New("algorithm"), str("ROUND_ROBIN")),
					statecheck.ExpectKnownValue("data.mtncloud_load_balancer_members.web", tfjsonpath.New("pool_id"), str(lbPoolID)),
					statecheck.ExpectKnownValue("data.mtncloud_load_balancer_members.web", tfjsonpath.New("members").AtSliceIndex(0).AtMapKey("address"), str("10.20.0.4")),
					statecheck.ExpectKnownValue("data.mtncloud_load_balancer_health_monitors.web", tfjsonpath.New("health_monitors").AtSliceIndex(0).AtMapKey("pool_id"), str(lbPoolID)),
					statecheck.ExpectKnownValue("data.mtncloud_load_balancer_health_monitor.web", tfjsonpath.New("id"), str(lbMonitor)),
					statecheck.ExpectKnownValue("data.mtncloud_load_balancer_health_monitor.web", tfjsonpath.New("delay"), knownvalue.Int64Exact(6)),
					statecheck.ExpectKnownValue("data.mtncloud_load_balancer_floating_ip_pools.net", tfjsonpath.New("pools").AtSliceIndex(0).AtMapKey("name"), str("private-cloud-1-dcnet1")),
					statecheck.ExpectKnownValue("data.mtncloud_load_balancer_floating_ip_pools.net", tfjsonpath.New("available_addresses"), knownvalue.ListSizeExact(3)),
				},
			},
			{Config: cfg, ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()}}},
		},
	})
	if task.reads == 0 {
		t.Fatal("no load balancer was read")
	}
}

func TestLoadBalancerDataSources_ClearErrors(t *testing.T) {
	for name, c := range map[string]struct{ config, want string }{
		"foreign network": {`
data "mtncloud_load_balancers" "x" {
  network_id = 999
}`, `Network 999 is not available to your tenant`},
		"unknown load balancer": {`
data "mtncloud_load_balancer_pools" "x" {
  network_id         = 114
  load_balancer_name = "nope"
}`, `No load balancer named 'nope'`},
		"other failure on an id": {`
data "mtncloud_load_balancer" "x" {
  network_id = 114
  id         = "00000000-0000-4000-8000-0000000000ee"
}`, `(?s)Unable to read the load balancer.*The cloud reported`},
		"unknown pool": {`
data "mtncloud_load_balancer_members" "x" {
  network_id         = 114
  load_balancer_name = "My Load Balancer"
  pool_name          = "nope"
}`, `no pool named "nope"`},
		"both selectors": {`
data "mtncloud_load_balancer_listener" "x" {
  network_id         = 114
  load_balancer_id   = "` + lbID + `"
  load_balancer_name = "My Load Balancer"
  name               = "HTTP-Listener"
}`, `(?s)Invalid Attribute Combination`},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFakeAPI(t)
			f.lb = newLBTask()
			resource.UnitTest(t, resource.TestCase{
				ProtoV6ProviderFactories: factories(),
				Steps:                    []resource.TestStep{{Config: f.providerConfig() + c.config, ExpectError: regexp.MustCompile(c.want)}},
			})
		})
	}
}
