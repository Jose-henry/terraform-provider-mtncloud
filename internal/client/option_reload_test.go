package client

import (
	"net/http"
	"sync/atomic"
	"testing"
)

// A name not in the cached option list is looked up once more, so a workflow made earlier in the same
// apply is found (review of the tester's report fixes, 2026-11-17); a name that isn't there costs one
// extra call, not more.
func TestOptionByName_ReloadsOnMiss(t *testing.T) {
	f := newFakeAPI(t)
	var calls atomic.Int32
	f.route(http.MethodGet, "/api/options/operationWorkflows", func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) == 1 {
			_, _ = w.Write([]byte(`{"data":[{"name":"old","value":21}]}`))
			return
		}
		_, _ = w.Write([]byte(`{"data":[{"name":"old","value":21},{"name":"tf-acc-wf","value":22}]}`))
	})
	c := f.client(t)
	if _, err := c.OptionByName(t.Context(), "operationWorkflows", "workflow", "old"); err != nil {
		t.Fatal(err)
	}
	o, err := c.OptionByName(t.Context(), "operationWorkflows", "workflow", "TF-ACC-WF")
	if err != nil || o.ID != 22 {
		t.Fatalf("got %+v, %v; want the new workflow 22", o, err)
	}
	if _, err := c.OptionByName(t.Context(), "operationWorkflows", "workflow", "nope"); err == nil {
		t.Error("nope: found")
	}
	if n := calls.Load(); n != 3 {
		t.Errorf("%d list calls, want 3 (first, reload for the new one, reload for the missing one)", n)
	}
}
