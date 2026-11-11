package client

import (
	"net/http"
	"testing"
)

// A node's newest failed build is explained by its failed step: the script the layout names, its
// phase and the platform's message (QA cluster 177's worker, 2026-11-11). An older failure, or
// no failure, doesn't count.
func TestNodeFailureOf(t *testing.T) {
	for _, tc := range []struct{ name, body, want string }{
		{"script", `{"processes":[
			{"status":"failed","dateCreated":"2026-11-10T10:00:00Z","events":[{"status":"failed","description":"old","processType":{"name":"execute script"},"error":"old"}]},
			{"status":"failed","dateCreated":"2026-11-11T00:17:13Z","processType":{"name":"server provision"},"error":"bash: line 1: null: command not found\n","events":[
				{"status":"complete","description":"kubernetes ubuntu setup script v1","processType":{"name":"execute script"}},
				{"status":"failed","description":"kubeadm worker join","processType":{"name":"execute script"},"error":"bash: line 1: null: command not found\n"},
				{"status":"pending","description":"setup-ceph-drivers.sh","processType":{"name":"execute script"}}]}]}`,
			"kubeadm worker join (execute script): bash: line 1: null: command not found"},
		{"no event failed", `{"processes":[{"status":"failed","dateCreated":"x","processType":{"name":"server provision"},"message":"Quota exceeded","events":[]}]}`,
			"server provision: Quota exceeded"},
		{"step without a description", `{"processes":[{"status":"failed","dateCreated":"x","events":[{"status":"failed","processType":{"name":"Prepare Resources"},"message":"no IP"}]}]}`,
			"Prepare Resources: no IP"},
		{"only the last lines", `{"processes":[{"status":"failed","dateCreated":"x","events":[{"status":"failed","description":"s","processType":{"name":"execute script"},"error":"a\nb\nc\nd\n"}]}]}`,
			"s (execute script): b c d"},
		{"nothing failed", `{"processes":[{"status":"complete","dateCreated":"x","events":[]}]}`, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeAPI(t)
			f.route(http.MethodGet, "/api/processes", func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Query().Get("serverId") != "6833" {
					t.Errorf("serverId %q", r.URL.Query().Get("serverId"))
				}
				_, _ = w.Write([]byte(tc.body))
			})
			got, err := f.client(t).NodeFailureOf(t.Context(), 6833)
			if err != nil {
				t.Fatal(err)
			}
			if got.String() != tc.want {
				t.Errorf("got %q, want %q", got.String(), tc.want)
			}
		})
	}
}
