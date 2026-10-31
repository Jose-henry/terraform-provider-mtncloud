package legacy

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	sdk "github.com/gomorpheus/morpheus-go-sdk"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
)

func TestNameLookupMessage(t *testing.T) {
	objs := []namedObject{{1, "web"}, {2, "db"}, {3, "WEB"}}
	for _, tc := range []struct {
		name, lookFor string
		objs          []namedObject
		listed        bool
		want          string
	}{
		{"none", "app", objs, true, `No budget is named "app". Names are matched exactly (case aside). Yours: WEB, db, web.`},
		{"several", "web", objs, true, `2 budgets are named "web" (IDs 1, 3). Give ` + "`id`" + ` instead of ` + "`name`" + ` to pick one.`},
		{"empty", "app", nil, true, `No budget is named "app". Names are matched exactly (case aside). You have none.`},
		{"list failed", "app", nil, false, `No budget is named "app". Names are matched exactly (case aside).`},
	} {
		if got := nameLookupMessage("budget", tc.lookFor, tc.objs, tc.listed); got != tc.want {
			t.Errorf("%s:\n got %s\nwant %s", tc.name, got, tc.want)
		}
	}

	var many []namedObject
	for i := range maxNamesShown + 3 {
		many = append(many, namedObject{int64(i), "n" + string(rune('a'+i))})
	}
	if got := nameLookupMessage("task", "x", many, true); !strings.HasSuffix(got, ", and 3 more.") {
		t.Errorf("long list: %s", got)
	}
}

// lookupServer answers every list call with body, and a get by ID with 404.
func lookupServer(t *testing.T, body string) *sdk.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		last := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
		if last != "" && last[0] >= '0' && last[0] <= '9' {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"success":false,"msg":"not found"}`))
			return
		}
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	c := sdk.NewClient(srv.URL)
	c.SetAccessToken("test-token", "", 3600, "write")
	return c
}

func readLookup(t *testing.T, r *schema.Resource, c *sdk.Client, attrs map[string]any) string {
	t.Helper()
	d := r.TestResourceData()
	for k, v := range attrs {
		if err := d.Set(k, v); err != nil {
			t.Fatal(err)
		}
	}
	diags := r.ReadContext(t.Context(), d, c)
	if !diags.HasError() {
		t.Fatalf("read succeeded (id %q), want an error", d.Id())
	}
	return diags[0].Summary
}

// The legacy lookups say what exists when a name matches nothing, give the IDs when several match,
// and fail (not succeed empty) on an ID that matches nothing.
func TestLegacyLookupMessages(t *testing.T) {
	// One list per response, as the API sends.
	none := lookupServer(t, `{"budgets":[],"meta":{"total":0}}`)
	two := lookupServer(t, `{"budgets":[{"id":4,"name":"Ops"},{"id":9,"name":"ops"}],"meta":{"total":2}}`)
	noKeys := lookupServer(t, `{"keyPairs":[],"meta":{"total":0}}`)
	twoKeys := lookupServer(t, `{"keyPairs":[{"id":5,"name":"deploy"},{"id":6,"name":"Deploy"}],"meta":{"total":2}}`)
	others := lookupServer(t, `{"tasks":[{"id":1,"name":"backup"},{"id":2,"name":"patch"}],"meta":{"total":2}}`)

	for _, tc := range []struct {
		name  string
		r     *schema.Resource
		c     *sdk.Client
		attrs map[string]any
		want  string
	}{
		{"budget none", dataSourceBudget(), none, map[string]any{"name": "ops"}, `No budget is named "ops". Names are matched exactly (case aside). You have none.`},
		{"budget two", dataSourceBudget(), two, map[string]any{"name": "ops"}, `2 budgets are named "ops" (IDs 4, 9).`},
		{"budget id", dataSourceBudget(), none, map[string]any{"id": 77}, `No budget has ID 77.`},
		// The list call during the lookup sees the other tasks, which the message offers.
		{"task none", dataSourceTask(), others, map[string]any{"name": "deploy"}, `No task is named "deploy".`},
		{"key pair none", dataSourceKeyPair(), noKeys, map[string]any{"name": "deploy"}, `No key pair is named "deploy".`},
		{"key pair two", dataSourceKeyPair(), twoKeys, map[string]any{"name": "deploy"}, `2 key pairs are named "deploy" (IDs 5, 6).`},
	} {
		if got := readLookup(t, tc.r, tc.c, tc.attrs); !strings.Contains(got, tc.want) {
			t.Errorf("%s:\n got %s\nwant %s", tc.name, got, tc.want)
		}
	}
}
