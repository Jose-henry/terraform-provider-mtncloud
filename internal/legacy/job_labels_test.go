package legacy

import (
	"net/http"
	"net/http/httptest"
	"testing"

	sdk "github.com/gomorpheus/morpheus-go-sdk"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
)

// A job read without labels sets labels to an empty set, so an imported job plans no change
// (live QA 2026-10-31: both job kinds planned labels as "known after apply" after import).
func TestJobReadSetsEmptyLabels(t *testing.T) {
	for _, target := range []string{"instance-label", "server-label"} {
		jobReadsEmpty(t, target)
	}
}

// jobReadsEmpty reads both job kinds with no labels and no targets left (the label cases indexed
// targets[0] and panicked).
func jobReadsEmpty(t *testing.T, target string) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"job":{"id":7,"name":"tf-acc-job","enabled":false,"targetType":"` + target + `",` +
			`"task":{"id":3},"workflow":{"id":4},"scheduleMode":"manual","targets":[]}}`))
	}))
	defer srv.Close()

	for name, r := range map[string]*schema.Resource{
		target + " task_job":     resourceTaskJob(),
		target + " workflow_job": resourceWorkflowJob(),
	} {
		c := sdk.NewClient(srv.URL)
		c.SetAccessToken("test-token", "", 3600, "write")
		d := r.TestResourceData()
		d.SetId("7")
		if diags := r.ReadContext(t.Context(), d, c); diags.HasError() {
			t.Fatalf("%s: read: %v", name, diags)
		}
		if n, ok := d.State().Attributes["labels.#"]; !ok || n != "0" {
			t.Errorf("%s: labels.# = %q (set %v), want an empty set", name, n, ok)
		}
	}
}
