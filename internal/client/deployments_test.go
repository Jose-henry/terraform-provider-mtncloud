package client

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// at is a whole-second time on the QA day, as MTN Cloud gives them.
func at(s string) time.Time {
	t, _ := time.Parse(time.RFC3339, "2026-11-01T"+s+"Z")
	return t
}

// proc is one process of an instance's history, as GET /api/processes lists it.
func proc(id int64, code, status, start string) map[string]any {
	return map[string]any{"id": id, "processType": map[string]any{"code": code}, "status": status, "startDate": start,
		"message": fmt.Sprintf("process %d", id)}
}

// history serves pages of an instance's processes, newest first, as GET /api/processes does.
func history(t *testing.T, f *fakeAPI, procs []map[string]any, calls *atomic.Int32) {
	f.route("GET", "/api/processes", func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Query().Get("instanceId") != "4661" {
			t.Errorf("instanceId = %q", r.URL.Query().Get("instanceId"))
		}
		var off, limit int
		_, _ = fmt.Sscan(r.URL.Query().Get("offset"), &off)
		_, _ = fmt.Sscan(r.URL.Query().Get("max"), &limit)
		end := min(off+limit, len(procs))
		page := []map[string]any{}
		if off < len(procs) {
			page = procs[off:end]
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"processes": page})
	})
}

// A deploy's run is the first deploy process that started at or after the deploy was made and
// before the next deploy: never an earlier process, another kind, or the next deploy's run.
func TestDeployRunFor(t *testing.T) {
	ds := func(created string) *InstanceDeploy {
		return &InstanceDeploy{ID: 11, InstanceID: 4661, Created: at(created)}
	}
	for _, tc := range []struct {
		name   string
		procs  []map[string]any
		d      *InstanceDeploy
		before time.Time
		want   int64 // 0: none
	}{
		{"its run, 5 s after", []map[string]any{proc(33, "deploy", "failed", "2026-11-01T07:36:15Z"), proc(32, "instanceWorkflow", "running", "2026-11-01T07:36:11Z")},
			ds("07:36:10"), time.Time{}, 33},
		{"a run that ended just before isn't its", []map[string]any{proc(31, "deploy", "complete", "2026-11-01T07:36:09Z")},
			ds("07:36:10"), time.Time{}, 0},
		{"the first after it, not a later one", []map[string]any{proc(35, "deploy", "complete", "2026-11-01T07:36:30Z"), proc(34, "deploy", "failed", "2026-11-01T07:36:14Z")},
			ds("07:36:10"), time.Time{}, 34},
		{"the next deploy's run isn't its", []map[string]any{proc(35, "deploy", "failed", "2026-11-01T07:36:16Z")},
			ds("07:36:10"), at("07:36:12"), 0},
		{"a date it can't read is skipped", []map[string]any{proc(36, "deploy", "failed", "01/11/2026 07:36:15"), proc(37, "deploy", "complete", "2026-11-01T07:36:20Z")},
			ds("07:36:10"), time.Time{}, 37},
		{"no time, no search", []map[string]any{proc(38, "deploy", "complete", "2026-11-01T07:36:15Z")},
			&InstanceDeploy{ID: 11, InstanceID: 4661}, time.Time{}, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeAPI(t)
			var calls atomic.Int32
			history(t, f, tc.procs, &calls)
			run, err := f.client(t).DeployRunFor(t.Context(), tc.d, tc.before)
			if err != nil {
				t.Fatal(err)
			}
			var got int64
			if run != nil {
				got = run.ID
			}
			if got != tc.want {
				t.Errorf("run %d, want %d", got, tc.want)
			}
			if tc.d.Created.IsZero() && calls.Load() != 0 {
				t.Errorf("%d history reads for a deploy with no time", calls.Load())
			}
		})
	}
}

// A run past the first page is found, and reading stops at the page that reaches older processes.
func TestDeployRunFor_Pages(t *testing.T) {
	var procs []map[string]any
	for i := range 150 { // newer processes of other kinds
		procs = append(procs, proc(int64(1000-i), "backup", "complete", "2026-11-01T08:00:00Z"))
	}
	procs = append(procs, proc(40, "deploy", "failed", "2026-11-01T07:36:15Z"), proc(39, "provision", "complete", "2026-11-01T07:19:26Z"))
	for i := range 300 { // older still: never read
		procs = append(procs, proc(int64(30-i), "deploy", "complete", "2026-11-01T06:00:00Z"))
	}
	f := newFakeAPI(t)
	var calls atomic.Int32
	history(t, f, procs, &calls)
	run, err := f.client(t).DeployRunFor(t.Context(), &InstanceDeploy{ID: 11, InstanceID: 4661, Created: at("07:36:10")}, time.Time{})
	if err != nil || run == nil || run.ID != 40 || run.Message != "process 40" {
		t.Fatalf("run %+v, err %v", run, err)
	}
	if done, failed := run.Done(); !done || !failed {
		t.Errorf("Done() = %v, %v", done, failed)
	}
	if calls.Load() != 2 {
		t.Errorf("%d pages read, want 2", calls.Load())
	}
}

// Waiting for a run: straight away with no time to go by, and nil once none has appeared in time.
func TestWaitForDeployRun(t *testing.T) {
	f := newFakeAPI(t)
	var calls atomic.Int32
	history(t, f, nil, &calls)
	c := f.client(t, func(cfg *Config) { cfg.PollMin, cfg.PollMax = time.Millisecond, 5*time.Millisecond })
	start := time.Now()
	if run, err := c.WaitForDeployRun(t.Context(), &InstanceDeploy{InstanceID: 4661}, time.Hour); run != nil || err != nil || time.Since(start) > time.Second {
		t.Fatalf("no time: %v, %v after %s", run, err, time.Since(start))
	}
	if run, err := c.WaitForDeployRun(t.Context(), &InstanceDeploy{InstanceID: 4661, Created: at("07:36:10")}, 50*time.Millisecond); run != nil || err != nil {
		t.Fatalf("none appeared: %v, %v", run, err)
	}
	if calls.Load() < 2 {
		t.Errorf("%d reads: it didn't wait", calls.Load())
	}
}

// A process read by ID; one that's gone is a not-found error.
func TestGetDeployRun(t *testing.T) {
	f := newFakeAPI(t)
	f.route("GET", "/api/processes/33", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"process": proc(33, "deploy", "complete", "2026-11-01T07:36:15Z")})
	})
	c := f.client(t)
	run, err := c.GetDeployRun(t.Context(), 33)
	if err != nil || run.ID != 33 || run.Status != "complete" || !run.Start.Equal(at("07:36:15")) {
		t.Fatalf("run %+v, err %v", run, err)
	}
	if _, err := c.GetDeployRun(t.Context(), 34); !IsNotFound(err) {
		t.Errorf("gone: %v", err)
	}
}

// A version "used in deploys" is deleted once MTN Cloud lets it; one it never lets go of says why.
func TestDeleteDeploymentVersion_InUse(t *testing.T) {
	f := newFakeAPI(t)
	var tries atomic.Int32
	inUse := func(w http.ResponseWriter) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"success":false,"msg":"Unable to delete version because it has been used in deploys."}`))
	}
	f.route("DELETE", "/api/deployments/20/versions/26", func(w http.ResponseWriter, _ *http.Request) {
		if tries.Add(1) < 3 {
			inUse(w)
			return
		}
		_, _ = w.Write([]byte(`{"success":true}`))
	})
	f.route("DELETE", "/api/deployments/20/versions/25", func(w http.ResponseWriter, _ *http.Request) { inUse(w) })
	f.route("DELETE", "/api/deployments/20/versions/24", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"success":false,"msg":"Something else is wrong."}`))
	})
	c := f.client(t, func(cfg *Config) {
		cfg.PollMin, cfg.PollMax, cfg.VersionInUseWait = time.Millisecond, 5*time.Millisecond, 100*time.Millisecond
	})
	if err := c.DeleteDeploymentVersion(t.Context(), 20, 26); err != nil || tries.Load() != 3 {
		t.Fatalf("err %v after %d tries", err, tries.Load())
	}
	if err := c.DeleteDeploymentVersion(t.Context(), 20, 25); err == nil || !strings.Contains(err.Error(), "delete those instances") {
		t.Errorf("never let go: %v", err)
	}
	start := time.Now()
	if err := c.DeleteDeploymentVersion(t.Context(), 20, 24); err == nil || strings.Contains(err.Error(), "delete those instances") || time.Since(start) > 50*time.Millisecond {
		t.Errorf("another 400 is returned straight away: %v after %s", err, time.Since(start))
	}
	if err := c.DeleteDeploymentVersion(t.Context(), 20, 23); err != nil { // gone already (the fake answers 404)
		t.Errorf("gone: %v", err)
	}
}

// A file goes up raw, named in the query, at the version's top level.
func TestUploadDeploymentFile(t *testing.T) {
	f := newFakeAPI(t)
	f.route("POST", "/api/deployments/20/versions/26/files", func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		if r.URL.Query().Get("filename") != "index.html" || string(b) != "<h1>hi</h1>" || r.Header.Get("Content-Type") != "application/octet-stream" {
			t.Errorf("sent %q %q %q", r.URL.Query().Get("filename"), b, r.Header.Get("Content-Type"))
		}
		_, _ = w.Write([]byte(`{"success":true}`))
	})
	if err := f.client(t).UploadDeploymentFile(t.Context(), 20, 26, "index.html", []byte("<h1>hi</h1>")); err != nil {
		t.Fatal(err)
	}
}

// A deploy's creation time is read, and the version comes from the nested object when the flat ID
// is missing.
func TestInstanceDeploys(t *testing.T) {
	f := newFakeAPI(t)
	f.route("GET", "/api/instances/4661/deploys", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"appDeploys":[{"id":11,"instanceId":4661,"deployment":{"id":20},"deploymentVersion":{"id":26,"userVersion":"2.0"},
			"status":"committed","dateCreated":"2026-11-01T07:36:10.000Z"}]}`))
	})
	ds, err := f.client(t).InstanceDeploys(t.Context(), 4661)
	if err != nil || len(ds) != 1 {
		t.Fatal(ds, err)
	}
	d := ds[0]
	if d.DeploymentID != 20 || d.VersionID != 26 || d.Version != "2.0" || !d.Created.Equal(at("07:36:10")) {
		t.Errorf("%+v", d)
	}
}

func TestDeployDone(t *testing.T) {
	for _, tc := range []struct {
		status       string
		done, failed bool
	}{{"deploying", false, false}, {"", false, false}, {"staged", false, false}, {"committed", true, false}, {"archived", true, false}, {"Failed", true, true}} {
		if done, failed := DeployDone(tc.status); done != tc.done || failed != tc.failed {
			t.Errorf("DeployDone(%q) = %v, %v", tc.status, done, failed)
		}
	}
	for _, tc := range []struct {
		status       string
		done, failed bool
	}{{"running", false, false}, {"complete", true, false}, {"failed", true, true}, {"cancelled", true, true}} { //nolint:misspell // the API's word
		if done, failed := (&DeployRun{Status: tc.status}).Done(); done != tc.done || failed != tc.failed {
			t.Errorf("run %q: %v, %v", tc.status, done, failed)
		}
	}
}

// A workflow runs on instances with its inputs, and its execution is waited for past a 404 straight
// after it starts.
func TestRunWorkflow(t *testing.T) {
	f := newFakeAPI(t)
	f.route("GET", "/api/task-sets", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"taskSets":[{"id":61,"name":"Create Linux User Git","type":"operation","optionTypes":[
			{"fieldName":"linuxUser","type":"text","required":true},{"fieldName":"linuxPassword","type":"password","required":true}]},
			{"id":62,"name":"Create Linux User Git v2","type":"operation"}]}`))
	})
	f.route("POST", "/api/task-sets/61/execute", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Job struct {
				Name          string            `json:"name"`
				TargetType    string            `json:"targetType"`
				Instances     []int64           `json:"instances"`
				CustomOptions map[string]string `json:"customOptions"`
			} `json:"job"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		if in.Job.TargetType != "instance" || fmt.Sprint(in.Job.Instances) != "[4661 4662]" || in.Job.CustomOptions["linuxUser"] != "deploy" ||
			!strings.HasPrefix(in.Job.Name, "tf-workflow-") {
			t.Errorf("sent %+v", in.Job)
		}
		_, _ = w.Write([]byte(`{"jobExecution":{"id":32320}}`))
	})
	var reads atomic.Int32
	f.route("GET", "/api/job-executions/32320", func(w http.ResponseWriter, _ *http.Request) {
		switch reads.Add(1) {
		case 1:
			w.WriteHeader(http.StatusNotFound)
		case 2:
			_, _ = w.Write([]byte(`{"jobExecution":{"id":32320,"status":"running"}}`))
		default:
			_, _ = w.Write([]byte(`{"jobExecution":{"id":32320,"status":"complete"}}`))
		}
	})
	c := f.client(t, func(cfg *Config) { cfg.PollMin, cfg.PollMax = time.Millisecond, 5*time.Millisecond })
	wfs, err := c.WorkflowsNamed(t.Context(), "create linux user git")
	if err != nil || len(wfs) != 1 || wfs[0].ID != 61 || len(wfs[0].Inputs) != 2 || !wfs[0].Inputs[1].Secret() || wfs[0].Inputs[0].Secret() {
		t.Fatalf("workflows %+v, err %v", wfs, err)
	}
	id, err := c.RunWorkflow(t.Context(), 61, []int64{4661, 4662}, map[string]string{"linuxUser": "deploy", "linuxPassword": "x"})
	if err != nil || id != 32320 {
		t.Fatal(id, err)
	}
	je, err := c.WaitForJobExecution(t.Context(), id)
	if err != nil || je.Status != "complete" || reads.Load() != 3 {
		t.Fatalf("%+v, %v after %d reads", je, err, reads.Load())
	}
}

// A cluster group's members are the ones tagged with its key, and nothing else in the pool.
func TestClusterMembers(t *testing.T) {
	if g := ClusterGroup(143, "kube-worker"); g != "cluster:143:kube-worker" {
		t.Fatal(g)
	}
	p := &LBPool{Members: []LBMember{
		{ID: "a", Tags: []string{"morpheus:group:cluster:143:kube-worker"}},  // mtncloud:allow-upstream-name (the plugin's tag)
		{ID: "b", Tags: []string{"morpheus:group:cluster:143:kube-master"}},  // mtncloud:allow-upstream-name (the plugin's tag)
		{ID: "c", Tags: []string{"morpheus:group:cluster:1430:kube-worker"}}, // mtncloud:allow-upstream-name (the plugin's tag)
		{ID: "d"},
	}}
	got := ClusterMembers(p, 143, "kube-worker")
	if len(got) != 1 || got[0].ID != "a" {
		t.Errorf("%+v", got)
	}
}
