package provider

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/knownvalue"
	"github.com/hashicorp/terraform-plugin-testing/statecheck"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"
)

// TestCypher_DataSource reads a string and an object key, and checks that a missing key is never
// read: a GET on a missing password/, uuid/ or key/ key would create it.
func TestCypher_DataSource(t *testing.T) {
	var mu sync.Mutex
	var reads []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch {
		case r.URL.Path == "/api/whoami":
			body, _ := os.ReadFile(filepath.Join("..", "client", "testdata", "lookups", "whoami_subtenant.json"))
			_, _ = w.Write(body)
		case r.URL.Path == "/api/cypher":
			_, _ = w.Write([]byte(`{"cyphers":[
 {"id":1,"itemKey":"secret/db","leaseTimeout":3600,"expireDate":"2026-11-01T00:00:00Z"},
 {"id":2,"itemKey":"password/20/admin","leaseTimeout":2764800,"expireDate":null}],"meta":{"total":2}}`))
		case strings.HasPrefix(r.URL.Path, "/api/cypher/"):
			mu.Lock()
			reads = append(reads, r.URL.Path)
			mu.Unlock()
			switch r.URL.Path {
			case "/api/cypher/secret/db":
				_, _ = w.Write([]byte(`{"success":true,"data":{"user":"app","password":"pw1"},"lease_duration":3600}`))
			case "/api/cypher/password/20/admin":
				_, _ = w.Write([]byte(`{"success":true,"data":"Xy9-generated","lease_duration":2764800}`))
			default:
				w.WriteHeader(http.StatusNotFound)
			}
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	cfg := `
provider "mtncloud" {
  url          = "` + srv.URL + `"
  access_token = "test-token"
}
`
	one := func(key string) string { return cfg + "data \"mtncloud_cypher\" \"x\" {\n  key = \"" + key + "\"\n}\n" }
	check := func(attr string, v knownvalue.Check) statecheck.StateCheck {
		return statecheck.ExpectKnownValue("data.mtncloud_cypher.x", tfjsonpath.New(attr), v)
	}
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories(),
		Steps: []resource.TestStep{
			{Config: one("secret/db"), ConfigStateChecks: []statecheck.StateCheck{
				check("values", knownvalue.MapExact(map[string]knownvalue.Check{"user": knownvalue.StringExact("app"), "password": knownvalue.StringExact("pw1")})),
				check("lease_timeout", knownvalue.Int64Exact(3600)),
				check("expire_date", knownvalue.StringExact("2026-11-01T00:00:00Z")),
			}},
			{Config: one("password/20/admin"), ConfigStateChecks: []statecheck.StateCheck{
				check("value", knownvalue.StringExact("Xy9-generated")),
				check("values", knownvalue.MapSizeExact(0)),
				check("expire_date", knownvalue.StringExact("")),
			}},
			{Config: one("uuid/missing"), ExpectError: regexp.MustCompile(`No such Cypher key`)},
			{Config: one("db"), ExpectError: regexp.MustCompile(`Invalid Cypher key`)},
			{Config: cfg},
		},
	})
	mu.Lock()
	defer mu.Unlock()
	for _, p := range reads {
		if strings.Contains(p, "missing") {
			t.Fatalf("read the missing key %s: that would create it", p)
		}
	}
}
