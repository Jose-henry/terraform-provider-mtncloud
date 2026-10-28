package provider

import (
	"encoding/json"
	"maps"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
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

// certStore is an in-memory certificate API. Like the platform, a read never returns the
// certificate or key text.
type certStore struct {
	mu    sync.Mutex
	next  int64
	certs map[int64]map[string]any
	sent  []map[string]any // each POST or PUT body's "certificate"
}

func newCertAPI(t *testing.T) (*certStore, string) {
	t.Helper()
	clearDefaults(t)
	s := &certStore{next: 9901, certs: map[int64]map[string]any{
		// A certificate request made in the console: readable, never managed here.
		9890: {"id": 9890, "name": "csr", "type": map[string]any{"id": 2, "code": "internalCsr"}},
	}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		reply := func(v any) { _ = json.NewEncoder(w).Encode(v) }
		body := func() map[string]any {
			var b struct {
				Certificate map[string]any `json:"certificate"`
			}
			_ = json.NewDecoder(r.Body).Decode(&b)
			s.sent = append(s.sent, maps.Clone(b.Certificate))
			return b.Certificate
		}
		shown := func(c map[string]any) map[string]any {
			out := map[string]any{}
			for k, v := range c {
				if k != "certFile" && k != "keyFile" && k != "chainFile" {
					out[k] = v
				}
			}
			return out
		}
		if r.URL.Path == "/api/certificates" {
			if r.Method == http.MethodPost {
				b := body()
				b["id"], b["type"], b["commonName"], b["selfSigned"] = s.next, map[string]any{"id": 1, "code": "internal"}, "shop.example.com", false
				s.certs[s.next] = b
				reply(map[string]any{"success": true, "certificate": b})
				s.next++
				return
			}
			var list []map[string]any
			for _, c := range s.certs {
				list = append(list, shown(c))
			}
			reply(map[string]any{"certificates": list, "meta": map[string]any{"total": len(list)}})
			return
		}
		id, _ := strconv.ParseInt(strings.TrimPrefix(r.URL.Path, "/api/certificates/"), 10, 64)
		c, ok := s.certs[id]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		switch r.Method {
		case http.MethodGet:
			reply(map[string]any{"certificate": shown(c)})
		case http.MethodPut:
			for k, v := range body() {
				c[k] = v
			}
			reply(map[string]any{"success": true, "certificate": c})
		case http.MethodDelete:
			delete(s.certs, id)
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

const testCert = "-----BEGIN CERTIFICATE-----\\nMIIB\\n-----END CERTIFICATE-----\\n"

func TestSSLCertificate(t *testing.T) {
	s, provider := newCertAPI(t)
	cfg := func(name, key string, version int) string {
		return provider + `
resource "mtncloud_ssl_certificate" "shop" {
  name                = "` + name + `"
  domain_name         = "shop.example.com"
  certificate         = "` + testCert + `"
  private_key         = "-----BEGIN PRIVATE KEY-----\n` + key + `\n-----END PRIVATE KEY-----\n"
  private_key_version = ` + strconv.Itoa(version) + `
}
`
	}
	sent := func(want string) func(*terraform.State) error {
		return func(*terraform.State) error {
			s.mu.Lock()
			defer s.mu.Unlock()
			last := s.sent[len(s.sent)-1]
			got := []string{}
			for _, k := range []string{"type", "name", "keyFile"} {
				if v, ok := last[k]; ok {
					got = append(got, k+"="+strings.ReplaceAll(strings.TrimSpace(v.(string)), "\n", "|"))
				}
			}
			if strings.Join(got, " ") != want {
				t.Errorf("sent %q, want %q", strings.Join(got, " "), want)
			}
			return nil
		}
	}
	addr := "mtncloud_ssl_certificate.shop"
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories(),
		Steps: []resource.TestStep{
			{
				Config: cfg("shop", "k1", 1),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(addr, tfjsonpath.New("id"), knownvalue.StringExact("9901")),
					statecheck.ExpectKnownValue(addr, tfjsonpath.New("private_key"), knownvalue.Null()),
					statecheck.ExpectKnownValue(addr, tfjsonpath.New("common_name"), knownvalue.StringExact("shop.example.com")),
					statecheck.ExpectKnownValue(addr, tfjsonpath.New("wildcard"), knownvalue.Bool(false)),
				},
				Check: sent("type=internal name=shop keyFile=-----BEGIN PRIVATE KEY-----|k1|-----END PRIVATE KEY-----"),
			},
			{Config: cfg("shop", "k1", 1), ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()}}},
			{
				// A rename is in place and doesn't send the key.
				Config: cfg("shop-2026", "k1", 1),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction(addr, plancheck.ResourceActionUpdate),
				}},
				Check: sent("name=shop-2026"),
			},
			{
				// A new key is noticed through private_key_version.
				Config: cfg("shop-2026", "k2", 2),
				Check:  sent("name=shop-2026 keyFile=-----BEGIN PRIVATE KEY-----|k2|-----END PRIVATE KEY-----"),
			},
			{
				Config: cfg("shop-2026", "k2", 2), ResourceName: addr, ImportState: true, ImportStateVerify: true,
				ImportStateVerifyIgnore: []string{"certificate", "private_key_version"},
			},
			{
				// Deleted in the console: planned again.
				PreConfig: func() {
					s.mu.Lock()
					delete(s.certs, 9901)
					s.mu.Unlock()
				},
				Config: cfg("shop-2026", "k2", 2),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction(addr, plancheck.ResourceActionCreate),
				}},
			},
			{
				// A second certificate with the same name is refused, never uploaded twice.
				Config:      cfg("shop-2026", "k2", 2) + strings.Replace(strings.TrimPrefix(cfg("shop-2026", "k2", 2), provider), `"shop"`, `"dup"`, 1),
				ExpectError: regexp.MustCompile(`already exists \(ID 9902\)`),
			},
		},
		CheckDestroy: func(*terraform.State) error {
			s.mu.Lock()
			defer s.mu.Unlock()
			for id := range s.certs {
				if id != 9890 {
					t.Errorf("certificate %d left behind", id)
				}
			}
			return nil
		},
	})
}

// Certificates made other ways (a request, a generated one) can't be managed.
func TestSSLCertificate_OnlyUploaded(t *testing.T) {
	_, provider := newCertAPI(t)
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories(),
		Steps: []resource.TestStep{{
			Config: provider + `
resource "mtncloud_ssl_certificate" "csr" {
  name        = "csr"
  certificate = "` + testCert + `"
  private_key = "-----BEGIN PRIVATE KEY-----\nk\n-----END PRIVATE KEY-----\n"
}
`,
			ResourceName: "mtncloud_ssl_certificate.csr", ImportState: true, ImportStateId: "9890",
			ExpectError: regexp.MustCompile(`type "internalCsr"`),
		}},
	})
}
