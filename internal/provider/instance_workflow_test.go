package provider

import (
	"encoding/json"
	"fmt"
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

// wfFake is MTN's workflows (the form of "Create Linux User Git", task set 61, read live 2026-11-01)
// and their runs. A run whose linuxUser is "bad" fails, printing the password it was given; one on
// instance 4663, made without the agent, fails with no output at all.
type wfFake struct {
	mu   sync.Mutex
	runs []map[string]any // each run's job: targetType, instances, customOptions
	next int64
}

func newWFFake(f *fakeAPI) *wfFake {
	w := &wfFake{next: 900}
	f.mu.Lock()
	f.extra, f.extraFor = w, func(p string) bool {
		return strings.HasPrefix(p, "/api/task-sets") || strings.HasPrefix(p, "/api/job-executions/") || p == "/api/instances/4663"
	}
	f.mu.Unlock()
	return w
}

func (wf *wfFake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	wf.mu.Lock()
	defer wf.mu.Unlock()
	write := func(v any) { b, _ := json.Marshal(v); _, _ = w.Write(b) }
	opt := func(f, t string, req bool) map[string]any {
		return map[string]any{"fieldName": f, "name": f, "type": t, "required": req, "defaultValue": nil}
	}
	sets := []map[string]any{
		{"id": 61, "name": "Create Linux User Git", "type": "operation", "optionTypes": []any{
			opt("linuxUser", "text", true), opt("linuxPassword", "password", true), opt("makeSudoer", "checkbox", false)}},
		{"id": 70, "name": "Build Base", "type": "provision", "optionTypes": []any{}},
		{"id": 71, "name": "Twin", "type": "operation"}, {"id": 72, "name": "twin", "type": "operation"},
	}
	switch {
	case r.URL.Path == "/api/instances/4663":
		write(map[string]any{"instance": map[string]any{"id": 4663, "name": "quiet", "status": "running", "config": map[string]any{"noAgent": true}}})
	case r.URL.Path == "/api/task-sets":
		list := []any{}
		for _, s := range sets {
			if strings.EqualFold(s["name"].(string), r.URL.Query().Get("name")) {
				list = append(list, s)
			}
		}
		write(map[string]any{"taskSets": list})
	case r.URL.Path == "/api/task-sets/61/execute":
		var in map[string]map[string]any
		_ = json.NewDecoder(r.Body).Decode(&in)
		wf.runs = append(wf.runs, in["job"])
		wf.next++
		write(map[string]any{"jobExecution": map[string]any{"id": wf.next}})
	case strings.HasPrefix(r.URL.Path, "/api/job-executions/"):
		var id int64
		_, _ = fmt.Sscanf(r.URL.Path, "/api/job-executions/%d", &id)
		job := wf.runs[id-901]
		opts := job["customOptions"].(map[string]any)
		status, events := "complete", []any{}
		if fmt.Sprint(job["instances"]) == "[4663]" {
			status = "error"
		} else if opts["linuxUser"] == "bad" {
			status = "failed"
			events = append(events, map[string]any{"status": "failed", "error": "useradd: invalid user name",
				"output": fmt.Sprintf("useradd -p %v bad\nexit 1", opts["linuxPassword"])})
		}
		write(map[string]any{"jobExecution": map[string]any{"id": id, "status": status, "process": map[string]any{"events": events}}})
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func TestInstanceWorkflow(t *testing.T) {
	f := newFakeAPI(t)
	wf := newWFFake(f)
	cfg := func(body string) string {
		return f.providerConfig() + `
resource "mtncloud_instance_workflow" "user" {
` + body + `}
`
	}
	good := func(user, round, timeout string) string {
		return cfg(fmt.Sprintf(`  workflow      = "create linux user git"
  instance_ids  = ["4661", "4662"]
  inputs        = { linuxUser = %q, makeSudoer = "on" }
  secret_inputs = { linuxPassword = "S3cret-pw-9!" }
  triggers      = { round = %q }
  timeouts { create = %q }
`, user, round, timeout))
	}
	const res = "mtncloud_instance_workflow.user"
	runs := func(n int) func(*terraform.State) error {
		return func(*terraform.State) error {
			wf.mu.Lock()
			defer wf.mu.Unlock()
			if len(wf.runs) != n {
				return fmt.Errorf("%d runs, want %d", len(wf.runs), n)
			}
			return nil
		}
	}
	empty := resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()}}
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories(),
		Steps: []resource.TestStep{
			// Refused at plan time, before anything runs.
			{Config: cfg(`  workflow = "No Such Workflow"
  instance_ids = ["4661"]
`), ExpectError: regexp.MustCompile(`No\s+workflow\s+named\s+"No\s+Such\s+Workflow"`)},
			{Config: cfg(`  workflow = "Build Base"
  instance_ids = ["4661"]
`), ExpectError: regexp.MustCompile(`is\s+a\s+provision\s+workflow`)},
			{Config: cfg(`  workflow = "twin"
  instance_ids = ["4661"]
`), ExpectError: regexp.MustCompile(`2\s+workflows\s+are\s+named\s+"twin"`)},
			{Config: cfg(`  workflow      = "Create Linux User Git"
  instance_ids  = ["4661"]
  inputs        = { linuxUser = "deploy", linuxPassword = "x", shell = "bash" }
`), ExpectError: regexp.MustCompile(`(?s)Secret in inputs.*put\s+it\s+in\s+secret_inputs.*no\s+field\s+"shell"`)},
			{Config: cfg(`  workflow      = "Create Linux User Git"
  instance_ids  = ["4661"]
  inputs        = { linuxUser = "deploy" }
`), ExpectError: regexp.MustCompile(`needs\s+"linuxPassword"\s+\(linuxPassword\)\s+in\s+secret_inputs`)},
			{Config: cfg(`  workflow     = "Create Linux User Git"
  instance_ids = ["web"]
`), ExpectError: regexp.MustCompile(`must\s+be\s+an\s+instance\s+ID`)},
			// Runs once on both instances, with every input; then a clean plan.
			{Config: good("deploy", "1", "30m"), Check: func(s *terraform.State) error {
				if err := runs(1)(s); err != nil {
					return err
				}
				wf.mu.Lock()
				defer wf.mu.Unlock()
				j := wf.runs[0]
				o := j["customOptions"].(map[string]any)
				if j["targetType"] != "instance" || fmt.Sprint(j["instances"]) != "[4661 4662]" || o["linuxPassword"] != "S3cret-pw-9!" || o["makeSudoer"] != "on" {
					return fmt.Errorf("sent %v", j)
				}
				return nil
			}, ConfigStateChecks: []statecheck.StateCheck{
				statecheck.ExpectKnownValue(res, tfjsonpath.New("status"), knownvalue.StringExact("complete")),
				statecheck.ExpectKnownValue(res, tfjsonpath.New("workflow_id"), knownvalue.Int64Exact(61)),
				statecheck.ExpectKnownValue(res, tfjsonpath.New("id"), knownvalue.StringExact("901")),
			}},
			{Config: good("deploy", "1", "30m"), ConfigPlanChecks: empty},
			// A new timeout changes nothing on the instances: no run.
			{Config: good("deploy", "1", "45m"), Check: runs(1)},
			// A trigger runs it again, in place.
			{Config: good("deploy", "2", "45m"), ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
				plancheck.ExpectResourceAction(res, plancheck.ResourceActionUpdate)}}, Check: runs(2),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(res, tfjsonpath.New("execution_id"), knownvalue.Int64Exact(902)),
					statecheck.ExpectKnownValue(res, tfjsonpath.New("id"), knownvalue.StringExact("901")),
				}},
			{Config: good("deploy", "2", "45m"), ConfigPlanChecks: empty},
			// A failed run names the reason, leaving out the line with the password (TestExecutionFailure
			// covers that), and the change stays to make.
			{Config: good("bad", "2", "45m"), ExpectError: regexp.MustCompile(`(?s)ended\s+"failed".*invalid\s+user\s+name\s+exit\s+1\.\s+The\s+next`)},
			{Config: good("bad", "2", "45m"), PlanOnly: true, ExpectNonEmptyPlan: true},
			// A run that fails with no output on a VM without the agent says that is why.
			{Config: strings.Replace(good("deploy", "2", "45m"), `["4661", "4662"]`, `["4663"]`, 1),
				ExpectError: regexp.MustCompile(`(?s)ended\s+"error".*Instance\s+4663\s+has\s+no\s+MTN\s+Cloud\s+agent`)},
			{Config: good("deploy", "2", "45m"), ConfigPlanChecks: empty},
		},
		CheckDestroy: runs(4), // destroy runs nothing
	})
}
