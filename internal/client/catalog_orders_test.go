package client

import (
	"net/http"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
)

func catalogFixture(t *testing.T, file string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "catalog", file))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestOrderWorkflow_Body(t *testing.T) {
	f := newFakeAPI(t)
	var body map[string]any
	f.route("POST", "/api/catalog/orders", captureBody(t, &body, http.StatusOK,
		[]byte(`{"success":true,"order":{"id":9800,"items":[{"id":9801,"type":{"id":10,"name":"Create Project","type":"workflow"},"status":"new"}]}}`)))
	it, err := f.client(t).OrderWorkflow(t.Context(), "Create Project", map[string]any{"projectName": "web"})
	if err != nil {
		t.Fatal(err)
	}
	if it.ID != 9801 || it.TypeName != "Create Project" {
		t.Errorf("item %+v", it)
	}
	item := body["order"].(map[string]any)["items"].([]any)[0].(map[string]any)
	if item["context"] != "appliance" || item["type"].(map[string]any)["name"] != "Create Project" ||
		item["config"].(map[string]any)["projectName"] != "web" {
		t.Errorf("order body %v", body)
	}
}

func TestOrderWorkflow_NoItem(t *testing.T) {
	f := newFakeAPI(t)
	f.route("POST", "/api/catalog/orders", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"success":true,"order":{"id":9800,"items":[]}}`))
	})
	if _, err := f.client(t).OrderWorkflow(t.Context(), "Create Project", nil); err == nil {
		t.Fatal("an order with no item must be an error")
	}
}

func TestWaitForExecution(t *testing.T) {
	for _, tc := range []struct {
		name, final string
		ok          bool
	}{
		{"success", "item_success.json", true},
		{"error", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeAPI(t)
			var reads atomic.Int32
			ordered, done := catalogFixture(t, "item_ordered.json"), catalogFixture(t, "item_success.json")
			if !tc.ok {
				done = []byte(`{"item":{"id":9801,"type":{"name":"Create Project"},"status":"ORDERED","execution":{"id":9811,"status":"error"}}}`)
			}
			f.route("GET", "/api/catalog/items/9801", func(w http.ResponseWriter, _ *http.Request) {
				switch reads.Add(1) {
				case 1:
					w.WriteHeader(http.StatusNotFound) // straight after the order
				case 2:
					_, _ = w.Write(ordered) // execution not started
				default:
					_, _ = w.Write(done)
				}
			})
			it, err := f.client(t).WaitForExecution(t.Context(), 9801)
			if err != nil {
				t.Fatal(err)
			}
			if d, ok := ExecutionDone(it.Execution); !d || ok != tc.ok {
				t.Errorf("execution %+v: done %v ok %v", it.Execution, d, ok)
			}
			if reads.Load() != 3 {
				t.Errorf("%d reads, want 3", reads.Load())
			}
		})
	}
}
