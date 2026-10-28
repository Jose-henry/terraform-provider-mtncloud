package provider

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/knownvalue"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/statecheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"
)

// cypherStore is an in-memory Cypher. Like the platform, a GET of a missing password/, uuid/ or
// key/ key creates it: createdByRead counts those, and the provider must never cause one.
type cypherStore struct {
	mu            sync.Mutex
	keys          map[string]string
	ttl           map[string]string
	generated     int
	createdByRead int
}

func newCypherAPI(t *testing.T) (*cypherStore, string) {
	t.Helper()
	clearDefaults(t)
	s := &cypherStore{keys: map[string]string{}, ttl: map[string]string{}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		reply := func(v any) { _ = json.NewEncoder(w).Encode(v) }
		if r.URL.Path == "/api/cypher" {
			var list []map[string]any
			for k := range s.keys {
				list = append(list, map[string]any{"itemKey": k, "leaseTimeout": 0})
			}
			reply(map[string]any{"cyphers": list, "meta": map[string]any{"total": len(list)}})
			return
		}
		key := strings.TrimPrefix(r.URL.Path, "/api/cypher/")
		switch r.Method {
		case http.MethodPost:
			s.generated++
			s.keys[key] = "generated-" + strings.Repeat("x", s.generated)
			s.ttl[key] = r.URL.Query().Get("ttl")
			reply(map[string]any{"success": true})
		case http.MethodGet:
			if _, ok := s.keys[key]; !ok {
				s.createdByRead++
				s.keys[key] = "created-by-read"
			}
			reply(map[string]any{"success": true, "data": s.keys[key], "lease_duration": 0})
		case http.MethodDelete:
			if _, ok := s.keys[key]; !ok {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			delete(s.keys, key)
			reply(map[string]any{"success": true})
		}
	}))
	t.Cleanup(srv.Close)
	return s, `
provider "mtncloud" {
  url          = "` + srv.URL + `"
  access_token = "test-token"
}
`
}

func TestCypherPassword(t *testing.T) {
	s, provider := newCypherAPI(t)
	cfg := provider + `
resource "mtncloud_cypher_password" "db" {
  name = "db-admin"
  ttl  = "8h"
}
`
	id := "password/15/db-admin"
	check := func(attr string, v knownvalue.Check) statecheck.StateCheck {
		return statecheck.ExpectKnownValue("mtncloud_cypher_password.db", tfjsonpath.New(attr), v)
	}
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories(),
		Steps: []resource.TestStep{
			{
				Config: cfg,
				ConfigStateChecks: []statecheck.StateCheck{
					check("id", knownvalue.StringExact(id)), check("length", knownvalue.Int64Exact(15)),
					check("value", knownvalue.StringExact("generated-x")),
				},
			},
			{Config: cfg, ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()}}},
			{Config: cfg, ResourceName: "mtncloud_cypher_password.db", ImportState: true, ImportStateVerify: true,
				ImportStateVerifyIgnore: []string{"ttl"}},
			{
				// Gone (deleted, or its lease ran out): planned again, never re-created by a read.
				PreConfig: func() {
					s.mu.Lock()
					clear(s.keys)
					s.mu.Unlock()
				},
				Config: cfg, PlanOnly: true, ExpectNonEmptyPlan: true,
			},
			{
				Config: cfg,
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction("mtncloud_cypher_password.db", plancheck.ResourceActionCreate),
				}},
			},
			{
				// A longer password is a new one.
				Config: strings.Replace(cfg, `ttl  = "8h"`, "ttl = \"8h\"\n  length = 32", 1),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction("mtncloud_cypher_password.db", plancheck.ResourceActionDestroyBeforeCreate),
				}},
			},
		},
		CheckDestroy: func(*terraform.State) error {
			s.mu.Lock()
			defer s.mu.Unlock()
			if len(s.keys) != 0 || s.createdByRead != 0 {
				t.Errorf("after destroy: keys %v, keys created by a read %d", s.keys, s.createdByRead)
			}
			return nil
		},
	})
}

func TestCypherKeyAndUUID(t *testing.T) {
	s, provider := newCypherAPI(t)
	cfg := provider + `
resource "mtncloud_cypher_key" "app" {
  name = "app"
  bits = 128
}
resource "mtncloud_cypher_uuid" "mac" {
  name = "mac-1"
  ttl  = "7d"
}
`
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories(),
		Steps: []resource.TestStep{
			{
				Config: cfg,
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue("mtncloud_cypher_key.app", tfjsonpath.New("id"), knownvalue.StringExact("key/128/app")),
					statecheck.ExpectKnownValue("mtncloud_cypher_uuid.mac", tfjsonpath.New("id"), knownvalue.StringExact("uuid/mac-1")),
				},
			},
			{Config: cfg, ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()}}},
			{Config: cfg, ResourceName: "mtncloud_cypher_key.app", ImportState: true, ImportStateVerify: true},
			// An import block with ttl in the configuration: the lease can't be read back, so the
			// plan records it in state (an update with no API call), and never makes a new key.
			{Config: cfg, ResourceName: "mtncloud_cypher_uuid.mac", ImportState: true, ImportStateKind: resource.ImportBlockWithID,
				ExpectNonEmptyPlan: true, ImportPlanChecks: resource.ImportPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction("mtncloud_cypher_uuid.mac", plancheck.ResourceActionUpdate),
				}}},
			{Config: cfg, ResourceName: "mtncloud_cypher_uuid.mac", ImportStateId: "secret/x", ImportState: true,
				ExpectError: regexp.MustCompile(`starting with uuid/`)},
			{Config: provider + "resource \"mtncloud_cypher_key\" \"app\" {\n  name = \"app\"\n  bits = 100\n}\n", PlanOnly: true,
				ExpectError: regexp.MustCompile(`bits`)},
			{Config: cfg}, // ends valid: the destroy after the test re-reads the last configuration
		},
	})
	if s.createdByRead != 0 {
		t.Errorf("%d keys created by a read", s.createdByRead)
	}
}

// An existing key is never overwritten: that would generate a new value under whatever uses it.
func TestCypherPassword_ExistingKeyIsRefused(t *testing.T) {
	s, provider := newCypherAPI(t)
	s.keys["password/15/db-admin"] = "in-use"
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories(),
		Steps: []resource.TestStep{{
			Config:      provider + "resource \"mtncloud_cypher_password\" \"db\" {\n  name = \"db-admin\"\n}\n",
			ExpectError: regexp.MustCompile(`(?s)already in Cypher.*terraform\s+import\s+<address>\s+password/15/db-admin`),
		}},
	})
	if s.keys["password/15/db-admin"] != "in-use" {
		t.Error("the existing key was overwritten")
	}
}
