package legacy

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	sdk "github.com/gomorpheus/morpheus-go-sdk"
)

// Create fills the defaults a permission set leaves out (MTN Cloud refuses a role without them);
// update leaves them out, so a default set in the console isn't reset to "none" by any change.
func TestUserRoleDefaultsOnCreateOnly(t *testing.T) {
	var (
		mu     sync.Mutex
		bodies = map[string]map[string]any{}
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method != http.MethodGet {
			raw, _ := io.ReadAll(r.Body)
			var b struct {
				Role map[string]any `json:"role"`
			}
			_ = json.Unmarshal(raw, &b)
			mu.Lock()
			bodies[r.Method] = b.Role
			mu.Unlock()
		}
		_, _ = w.Write([]byte(`{"success":true,"role":{"id":5,"authority":"r","defaultPersona":{"code":"standard"}}}`))
	}))
	defer srv.Close()

	r := resourceUserRole()
	c := sdk.NewClient(srv.URL)
	c.SetAccessToken("test-token", "", 3600, "write")
	d := r.TestResourceData()
	_ = d.Set("name", "r")
	_ = r.CreateContext(t.Context(), d, c)
	// An update from a config with no permission_set.
	u := r.TestResourceData()
	u.SetId("5")
	_ = u.Set("name", "r")
	_ = u.Set("description", "changed")
	_ = r.UpdateContext(t.Context(), u, c)

	mu.Lock()
	defer mu.Unlock()
	if got := bodies[http.MethodPost]["globalSiteAccess"]; got != "none" {
		t.Errorf("create sent globalSiteAccess %v, want none", got)
	}
	for _, k := range []string{"globalSiteAccess", "globalTaskAccess", "defaultPersona"} {
		if got, sent := bodies[http.MethodPut][k]; sent {
			t.Errorf("update sent %s = %v, want it left out so the console's value stays", k, got)
		}
	}
	if got := bodies[http.MethodPost]["defaultPersona"]; got == nil {
		t.Error("create sent no defaultPersona, want standard")
	}
	if bodies[http.MethodPut] == nil {
		t.Error("no update sent")
	}
}
