package client

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// lbFake serves the task list, the execute call and the job executions. outputs are the plugin
// answers in order: a job execution fixture's name under testdata/lb, or a raw JSON envelope (or
// "" for a job that ends in error with no output). The last one repeats.
type lbFake struct {
	*fakeAPI
	runs atomic.Int64
	opts []map[string]any
}

func newLBFake(t *testing.T, outputs ...string) *lbFake {
	t.Helper()
	f := &lbFake{fakeAPI: newFakeAPI(t)}
	f.route("GET", "/api/tasks", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"tasks":[{"id":259,"code":"iportroutek123456"},{"id":450,"code":"lboperate.terraform"}],"meta":{"total":2}}`))
	})
	f.route("POST", "/api/tasks/450/execute", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Job struct {
				TargetType    string         `json:"targetType"`
				CustomOptions map[string]any `json:"customOptions"`
			} `json:"job"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body.Job.TargetType != "appliance" {
			t.Errorf("targetType %q", body.Job.TargetType)
		}
		f.mu.Lock()
		f.opts = append(f.opts, body.Job.CustomOptions)
		f.mu.Unlock()
		n := f.runs.Add(1)
		_, _ = w.Write([]byte(`{"success":true,"jobExecution":{"id":` + itoa(31000+n) + `}}`))
	})
	for i := range max(6, len(outputs)) {
		out := outputs[min(i, len(outputs)-1)]
		f.route("GET", "/api/job-executions/"+itoa(int64(31001+i)), func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write(lbJob(t, out))
		})
	}
	return f
}

// lbJob is a job execution answer: a recorded fixture, or one built around an envelope.
func lbJob(t *testing.T, out string) []byte {
	t.Helper()
	if b, err := os.ReadFile(filepath.Join("testdata", "lb", out+".json")); err == nil {
		return b
	}
	status, events := "success", []any{map[string]any{"processType": map[string]any{"code": "executeTask"}, "output": out}}
	if out == "" {
		status, events = "error", []any{}
	} else if json.Valid([]byte(out)) {
		var env lbEnvelope
		_ = json.Unmarshal([]byte(out), &env)
		if !env.Success {
			status = "error"
		}
	}
	b, _ := json.Marshal(map[string]any{"jobExecution": map[string]any{"id": 31001, "status": status, "process": map[string]any{"events": events}}})
	return b
}

func TestListLoadBalancers(t *testing.T) {
	f := newLBFake(t, "list")
	got, err := f.client(t, fastPoll).ListLoadBalancers(t.Context(), 114, LBListFilter{Status: "active"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Total != 2 || len(got.LoadBalancers) != 2 || !got.CanManage {
		t.Fatalf("list %+v", got)
	}
	lb := got.LoadBalancers[0]
	if lb.Name != "My Load Balancer" || lb.ProvisioningStatus != "ACTIVE" || lb.FloatingIP == "" || !lb.FloatingIPKnown || lb.VIPAddress == "" {
		t.Errorf("row %+v", lb)
	}
	// Inputs go in customOptions, as strings; never an account or tenant.
	o := f.opts[0]
	if o["action"] != "list" || o["networkId"] != "114" || o["status"] != "active" {
		t.Errorf("options %v", o)
	}
	for _, k := range []string{"accountId", "tenant", "userId"} {
		if _, ok := o[k]; ok {
			t.Errorf("sent %s", k)
		}
	}
}

func TestGetLoadBalancer(t *testing.T) {
	f := newLBFake(t, "read")
	lb, canManage, err := f.client(t, fastPoll).GetLoadBalancer(t.Context(), 114, "", "My Load Balancer")
	if err != nil {
		t.Fatal(err)
	}
	if !canManage || lb.Name != "My Load Balancer" || len(lb.Listeners) != 2 || len(lb.Pools) != 1 {
		t.Fatalf("lb %+v", lb)
	}
	p := lb.Pools[0]
	if len(p.Members) != 3 || p.HealthMonitor == nil || p.HealthMonitor.Type == "" || p.Algorithm == "" || p.Members[0].Port == 0 {
		t.Errorf("pool %+v", p)
	}
	if l := lb.Listeners[0]; l.Protocol == "" || l.Port == 0 {
		t.Errorf("listener %+v", l)
	}
	if f.opts[0]["name"] != "My Load Balancer" || f.opts[0]["lbId"] != nil {
		t.Errorf("options %v", f.opts[0])
	}
}

func TestLBFloatingIPChoices(t *testing.T) {
	f := newLBFake(t, "fip")
	got, err := f.client(t, fastPoll).LBFloatingIPChoices(t.Context(), 114)
	if err != nil || len(got.Pools) == 0 || got.Pools[0].Name == "" || got.Pools[0].ExternalID == "" {
		t.Fatalf("%+v, err %v", got, err)
	}
}

func TestRunLB_RetriesWhatDidNotRun(t *testing.T) {
	stall := `{"success":false,"action":null,"error":"RuntimeException: java.util.concurrent.TimeoutException: The source did not signal an event for 15000 milliseconds and has been terminated."}`
	busy := `{"success":false,"action":"list","retryable":true,"error":"The console did not answer in time, so nothing was read or changed. Try again in a moment."}`
	for name, outputs := range map[string][]string{
		"input timeout": {stall, "list"},
		"no output":     {"", "list"},
		"retryable":     {busy, "list"},
	} {
		t.Run(name, func(t *testing.T) {
			f := newLBFake(t, outputs...)
			if _, err := f.client(t, fastPoll).ListLoadBalancers(t.Context(), 114, LBListFilter{}); err != nil {
				t.Fatal(err)
			}
			if f.runs.Load() != 2 {
				t.Errorf("runs %d, want 2", f.runs.Load())
			}
		})
	}
	// Bounded: a run that never works stops after lbRetries retries.
	f := newLBFake(t, stall)
	if _, err := f.client(t, fastPoll).ListLoadBalancers(t.Context(), 114, LBListFilter{}); err == nil {
		t.Fatal("no error")
	}
	if f.runs.Load() != lbRetries+1 {
		t.Errorf("runs %d, want %d", f.runs.Load(), lbRetries+1)
	}
}

func TestRunLB_Refusals(t *testing.T) {
	for _, c := range []struct {
		name, out              string
		notAvailable, notFound bool
	}{
		{"foreign network", `{"success":false,"action":"read","error":"Network 999 is not available to your tenant."}`, true, false},
		{"gone", `{"success":false,"action":"read","error":"Load Balancer 00000000-0000-4000-8000-000000000009 not found."}`, false, true},
		{"other network", `{"success":false,"action":"read","error":"Load balancer 00000000-0000-4000-8000-000000000009 was not found on this network."}`, false, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newLBFake(t, c.out)
			_, _, err := f.client(t, fastPoll).GetLoadBalancer(t.Context(), 114, "00000000-0000-4000-8000-000000000009", "")
			var lbErr *LBError
			if !errors.As(err, &lbErr) || IsLBNotAvailable(err) != c.notAvailable || IsLBNotFound(err) != c.notFound {
				t.Fatalf("err %v", err)
			}
			if f.runs.Load() != 1 {
				t.Errorf("a refusal was retried: %d runs", f.runs.Load())
			}
		})
	}
}

func TestRunLB_NoTaskAccess(t *testing.T) {
	f := newLBFake(t, "list")
	f.route("POST", "/api/tasks/450/execute", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusForbidden) })
	if _, err := f.client(t, fastPoll).ListLoadBalancers(t.Context(), 114, LBListFilter{}); !errors.Is(err, ErrLBAccess) {
		t.Fatalf("err %v", err)
	}
	g := newLBFake(t, "list")
	g.route("GET", "/api/tasks", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"tasks":[],"meta":{"total":0}}`))
	})
	if _, err := g.client(t, fastPoll).ListLoadBalancers(t.Context(), 114, LBListFilter{}); !errors.Is(err, ErrLBAccess) {
		t.Fatalf("no task: err %v", err)
	}
}

func TestLoadBalancerSnapshot_OneReadPerRun(t *testing.T) {
	f := newLBFake(t, "read")
	c := f.client(t, fastPoll)
	for range 3 {
		if _, _, err := c.LoadBalancerSnapshot(t.Context(), 114, "", "My Load Balancer"); err != nil {
			t.Fatal(err)
		}
	}
	if f.runs.Load() != 1 {
		t.Errorf("runs %d, want 1", f.runs.Load())
	}
	// A different selector is a different read.
	if _, _, err := c.LoadBalancerSnapshot(t.Context(), 114, "00000000-0000-4000-8000-000000000006", ""); err != nil || f.runs.Load() != 2 {
		t.Errorf("runs %d, err %v", f.runs.Load(), err)
	}
}

func TestRunLB_ReadLookupFailedIsRetried(t *testing.T) {
	// Reads report a console that didn't answer inside result, not at the top level.
	busy := `{"success":false,"action":"read","error":"The console did not return the details of this network or instance in time, so nothing was read or changed. Try again in a moment.","result":{"lookupFailed":true,"retryable":true}}`
	f := newLBFake(t, busy, "read")
	if _, _, err := f.client(t, fastPoll).GetLoadBalancer(t.Context(), 114, "", "My Load Balancer"); err != nil {
		t.Fatal(err)
	}
	if f.runs.Load() != 2 {
		t.Errorf("runs %d, want 2", f.runs.Load())
	}
}

func TestRunLB_StalledJobIsBounded(t *testing.T) {
	f := newLBFake(t, "list")
	for i := range 6 {
		f.route("GET", "/api/job-executions/"+itoa(int64(31001+i)), func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"jobExecution":{"id":1,"status":"running","process":{"events":[]}}}`))
		})
	}
	short := func(c *Config) { fastPoll(c); c.LBAttempt = 30 * time.Millisecond }
	_, err := f.client(t, short).ListLoadBalancers(t.Context(), 114, LBListFilter{})
	var lbErr *LBError
	if !errors.As(err, &lbErr) || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("err %v", err)
	}
	if f.runs.Load() != lbRetries+1 {
		t.Errorf("runs %d, want %d", f.runs.Load(), lbRetries+1)
	}
}

func TestRunLB_JobThatNeverAppears(t *testing.T) {
	f := newLBFake(t, "list")
	for i := range 6 {
		f.route("GET", "/api/job-executions/"+itoa(int64(31001+i)), func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNotFound) })
	}
	if _, err := f.client(t, fastPoll).ListLoadBalancers(t.Context(), 114, LBListFilter{}); !errors.Is(err, ErrLBAccess) {
		t.Fatalf("err %v", err)
	}
}

func TestRunLB_EmptyAnswerIsAnError(t *testing.T) {
	for name, out := range map[string]string{
		"null result":   `{"success":true,"action":"list","result":null}`,
		"no list in it": `{"success":true,"action":"list","result":{"total":0}}`,
	} {
		t.Run(name, func(t *testing.T) {
			f := newLBFake(t, out)
			if got, err := f.client(t, fastPoll).ListLoadBalancers(t.Context(), 114, LBListFilter{}); err == nil {
				t.Fatalf("no error, got %+v", got)
			}
		})
	}
}

func TestLBCustomerText(t *testing.T) {
	for in, want := range map[string]string{
		"Cannot determine the OpenStack project id for 'x'.": "Cannot determine the cloud project id for 'x'.", // mtncloud:allow-cloud-name (input)
		"OpenStack reported load balancer x in ERROR.":       "The cloud reported load balancer x in ERROR.",   // mtncloud:allow-cloud-name (input)
		"Keystone auth failed with HTTP 401":                 "the cloud's sign-in auth failed with HTTP 401",  // mtncloud:allow-cloud-name (input)
	} {
		if got := lbCustomerText(in); got != want {
			t.Errorf("%q: %q, want %q", in, got, want)
		}
	}
}

func TestRunLB_WriteTimeoutIsNotRetried(t *testing.T) {
	for _, action := range []string{"pool-update", "member-remove", "listener-update", "loadbalancer-update", "delete"} {
		if lbTransient(action, lbTimedOut, nil) || lbTransient(action, "error", nil) {
			t.Errorf("%s: a write that may have acted counted as nothing ran", action)
		}
	}
	// Creates carry a requestId: a retry finds what the first try made (guide 9.6).
	for _, action := range []string{"create", "listener-create", "pool-create", "member-add", "monitor-create", "l7policy-create"} {
		if !lbTransient(action, lbTimedOut, nil) || !lbTransient(action, "error", nil) {
			t.Errorf("a timed-out %s wasn't retried: its requestId makes that safe", action)
		}
	}
	if !lbTransient("read", lbTimedOut, nil) {
		t.Error("a timed-out read wasn't retried")
	}
}

func TestRunLB_MessagesNeverNameTheCloudSoftware(t *testing.T) {
	f := newLBFake(t, `{"success":false,"action":"read","error":"Cannot determine the OpenStack project id for 'x'. Octavia said no."}`) // mtncloud:allow-cloud-name (the plugin's wording)
	_, _, err := f.client(t, fastPoll).GetLoadBalancer(t.Context(), 114, "id-1", "")
	if err == nil || strings.Contains(strings.ToLower(err.Error()), "openstack") || strings.Contains(strings.ToLower(err.Error()), "octavia") { // mtncloud:allow-cloud-name (checking it is gone)
		t.Fatalf("err %v", err)
	}
	if IsLBMissing(err) {
		t.Errorf("a cloud failure counted as a missing load balancer")
	}
}

func TestIsLBMissing(t *testing.T) {
	for msg, want := range map[string]bool{
		"Load Balancer 00000000-0000-4000-8000-000000000009 not found.":                  true,
		"Load balancer x was not found on this network.":                                 true,
		"No load balancer named 'web' on network 114.":                                   true,
		"2 load balancers on network 114 are named 'web' (IDs a, b). Look one up by ID.": true,
		"The load balancer read operation ended (failed) without an answer. Try again.":  false,
		"the cloud reported load balancer x in ERROR.":                                   false,
		"Cloud with ID 4 not found":                                                      false,
		"The resource could not be found.":                                               true,
	} {
		if got := IsLBMissing(&LBError{Msg: msg}); got != want {
			t.Errorf("%q: %v, want %v", msg, got, want)
		}
	}
}
