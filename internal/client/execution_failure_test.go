package client

import (
	"net/http"
	"strings"
	"testing"
)

// A failed workflow's reason is its last output lines, never one that mentions a secret.
func TestLastLines(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"a\n\nError: kafkaVersion must be 4.3.1\nNothing was provisioned.\n", "a Error: kafkaVersion must be 4.3.1 Nothing was provisioned."},
		{"x\ny\nz\nw", "y z w"},
		{"Error: bad\nADMIN_PASSWORD=hunter2\n", "Error: bad"},
		{"  \n", ""},
	} {
		if got := lastLines(tc.in, 3); got != tc.want {
			t.Errorf("lastLines(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
	if got := lastLines(strings.Repeat("x", 900), 3); len(got) != 500 || !strings.HasPrefix(got, "...") {
		t.Errorf("long output: %d characters", len(got))
	}
}

// The reason is the failed task's, past a failed task set with no output, with the order's own
// secret values masked and their lines left out.
func TestExecutionFailure(t *testing.T) {
	f := newFakeAPI(t)
	f.route("GET", "/api/job-executions/32106", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"jobExecution":{"process":{"events":[
			{"status":"complete","output":"Validating"},
			{"status":"failed","output":""},
			{"status":"failed","output":"connecting as admin with Kq9-zz81\nError: Invalid msg_engine value 'null'."}]}}}`))
	})
	c := f.client(t)
	got := c.ExecutionFailure(t.Context(), 32106, []string{"Kq9-zz81"})
	if got != "Error: Invalid msg_engine value 'null'." {
		t.Errorf("got %q", got)
	}
	if c.ExecutionFailure(t.Context(), 0, nil) != "" || c.ExecutionFailure(t.Context(), 404, nil) != "" {
		t.Error("no execution should say nothing")
	}
}
