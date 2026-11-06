package client

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeAPI is an httptest server that replays scrubbed fixtures from testdata/ and records calls.
type fakeAPI struct {
	t      *testing.T
	srv    *httptest.Server
	mu     sync.Mutex
	calls  []*http.Request
	routes map[string]http.HandlerFunc // "GET /api/whoami" -> handler
}

func newFakeAPI(t *testing.T) *fakeAPI {
	t.Helper()
	f := &fakeAPI{t: t, routes: map[string]http.HandlerFunc{}}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.calls = append(f.calls, r.Clone(context.Background()))
		h, ok := f.routes[r.Method+" "+r.URL.Path]
		f.mu.Unlock()
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"success":false,"msg":"no route in fake API"}`))
			return
		}
		h(w, r)
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeAPI) fixture(method, path, file string) {
	body, err := os.ReadFile(filepath.Join("testdata", "lookups", file))
	if err != nil {
		f.t.Fatal(err)
	}
	f.route(method, path, func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(body) })
}

func (f *fakeAPI) route(method, path string, h http.HandlerFunc) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.routes[method+" "+path] = h
}

func (f *fakeAPI) count(method, path string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, r := range f.calls {
		if r.Method == method && r.URL.Path == path {
			n++
		}
	}
	return n
}

func (f *fakeAPI) last(path string) *http.Request {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := len(f.calls) - 1; i >= 0; i-- {
		if f.calls[i].URL.Path == path {
			return f.calls[i]
		}
	}
	return nil
}

func (f *fakeAPI) client(t *testing.T, mod ...func(*Config)) *Client {
	t.Helper()
	cfg := Config{URL: f.srv.URL, Token: "test-token", BackoffBase: time.Millisecond, BackoffMax: 5 * time.Millisecond, RequestsPerSec: 1000, Burst: 1000}
	for _, m := range mod {
		m(&cfg)
	}
	c, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// withPlacement registers the group and cloud lookups every scoped call needs.
func (f *fakeAPI) withPlacement() {
	f.fixture("GET", "/api/options/groups", "options_groups.json")
	f.fixture("GET", "/api/options/clouds", "options_clouds.json")
}

func TestMayHaveSucceeded(t *testing.T) {
	for err, want := range map[error]bool{
		&APIError{StatusCode: 502}:                          true,
		&APIError{StatusCode: 500}:                          true,
		&APIError{StatusCode: 400}:                          false,
		&APIError{StatusCode: 404}:                          false,
		errors.New("connection reset"):                      true,
		context.Canceled:                                    false,
		fmt.Errorf("wrapped: %w", context.DeadlineExceeded): false,
	} {
		if got := MayHaveSucceeded(err); got != want {
			t.Errorf("MayHaveSucceeded(%v) = %v, want %v", err, got, want)
		}
	}
	if MayHaveSucceeded(nil) {
		t.Error("nil error")
	}
}

func TestNew_Validates(t *testing.T) {
	for name, cfg := range map[string]Config{
		"no url":      {Token: "x"},
		"relative":    {URL: "console.cloud.mtn.ng", Token: "x"},
		"no creds":    {URL: "https://console.cloud.mtn.ng"},
		"no password": {URL: "https://console.cloud.mtn.ng", Username: "u"},
	} {
		if _, err := New(cfg); err == nil {
			t.Errorf("%s: want error", name)
		}
	}
}

func TestLogin_PasswordGrantWithTenant(t *testing.T) {
	f := newFakeAPI(t)
	f.route("POST", "/oauth/token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if r.PostForm.Get("username") != `acme\jdoe` || r.PostForm.Get("grant_type") != "password" || r.PostForm.Get("password") != "pw" { // ggignore: fake test credential
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"invalid_grant"}`))
			return
		}
		_, _ = w.Write([]byte(`{"access_token":"fresh"}`))
	})
	f.route("GET", "/api/whoami", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer fresh" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		body, _ := os.ReadFile(filepath.Join("testdata", "lookups", "whoami_subtenant.json"))
		_, _ = w.Write(body)
	})
	c := f.client(t, func(c *Config) { c.Token, c.Username, c.Password, c.Tenant = "", "jdoe", "pw", "acme" }) // ggignore: fake test credential
	who, err := c.WhoAmI(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if who.AccountName != "Acme" || who.IsMaster {
		t.Errorf("got %+v", who)
	}
	if _, err := c.WhoAmI(t.Context()); err != nil {
		t.Fatal(err)
	}
	if n := f.count("POST", "/oauth/token"); n != 1 {
		t.Errorf("logged in %d times, want 1", n)
	}
	if n := f.count("GET", "/api/whoami"); n != 1 {
		t.Errorf("whoami called %d times, want 1 (cached)", n)
	}
}

// The tenant can be the numeric account ID instead of the subdomain (checked live, 2026-10-29).
func TestLogin_TenantByAccountID(t *testing.T) {
	f := newFakeAPI(t)
	var got string
	f.route("POST", "/oauth/token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		got = r.PostForm.Get("username")
		_, _ = w.Write([]byte(`{"access_token":"fresh"}`))
	})
	f.route("GET", "/api/whoami", func(w http.ResponseWriter, _ *http.Request) {
		body, _ := os.ReadFile(filepath.Join("testdata", "lookups", "whoami_subtenant.json"))
		_, _ = w.Write(body)
	})
	c := f.client(t, func(c *Config) { c.Token, c.Username, c.Password, c.Tenant = "", "jdoe", "pw", "42" }) // ggignore: fake test credential
	if _, err := c.WhoAmI(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got != `42\jdoe` {
		t.Errorf("username %q, want 42\\jdoe", got)
	}
}

func TestLogin_WrongPasswordIsClear(t *testing.T) {
	f := newFakeAPI(t)
	f.route("POST", "/oauth/token", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"invalid_grant"}`))
	})
	c := f.client(t, func(c *Config) { c.Token, c.Username, c.Password = "", "jdoe", "bad" })
	_, err := c.WhoAmI(t.Context())
	if err == nil || !strings.Contains(err.Error(), "wrong username, password or tenant (subdomain or account ID)") {
		t.Fatalf("got %v", err)
	}
	if strings.Contains(err.Error(), "bad") {
		t.Error("error leaks the password")
	}
}

func TestRelogin_OnExpiredToken(t *testing.T) {
	f := newFakeAPI(t)
	var logins atomic.Int32
	f.route("POST", "/oauth/token", func(w http.ResponseWriter, _ *http.Request) {
		if logins.Add(1) == 1 {
			_, _ = w.Write([]byte(`{"access_token":"old"}`))
			return
		}
		_, _ = w.Write([]byte(`{"access_token":"new"}`))
	})
	f.route("GET", "/api/whoami", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer new" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(`{"user":{"account":{"id":1,"name":"Acme"}}}`))
	})
	c := f.client(t, func(c *Config) { c.Token, c.Username, c.Password = "", "jdoe", "pw" }) // ggignore: fake test credential
	if _, err := c.WhoAmI(t.Context()); err != nil {
		t.Fatal(err)
	}
	if logins.Load() != 2 {
		t.Errorf("logins = %d, want 2", logins.Load())
	}
}

func TestRetry(t *testing.T) {
	cases := []struct {
		name      string
		method    string
		statuses  []int
		body      string // the error body; {} when empty
		wantCalls int
		wantErr   bool
	}{
		{"GET 503 then ok", "GET", []int{503, 503, 200}, "", 3, false},
		{"GET 429 then ok", "GET", []int{429, 200}, "", 2, false},
		{"GET 500 gives up", "GET", []int{500, 500, 500, 500, 500, 500}, "", 5, true},
		{"GET 400 never retried", "GET", []int{400, 200}, "", 1, true},
		{"GET 404 never retried", "GET", []int{404, 200}, "", 1, true},
		{"POST 500 not retried", "POST", []int{500, 200}, "", 1, true},
		{"POST 429 retried", "POST", []int{429, 200}, "", 2, false},
		// A label race isn't retried here: only a cluster create retries it, after a lookup.
		{"POST 400 label race not retried", "POST", []int{400, 200}, `{"success":false,"msg":"Duplicate entry '72-tf-acc' for key 'label.UK1'"}`, 1, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeAPI(t)
			var n atomic.Int32
			f.route(tc.method, "/api/thing", func(w http.ResponseWriter, _ *http.Request) {
				i := int(n.Add(1)) - 1
				st := tc.statuses[min(i, len(tc.statuses)-1)]
				w.WriteHeader(st)
				if st >= 400 && tc.body != "" {
					_, _ = w.Write([]byte(tc.body))
					return
				}
				_, _ = w.Write([]byte(`{}`))
			})
			c := f.client(t)
			err := c.do(t.Context(), tc.method, "/api/thing", nil, map[string]string{}, nil)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tc.wantErr)
			}
			if got := f.count(tc.method, "/api/thing"); got != tc.wantCalls {
				t.Errorf("calls = %d, want %d", got, tc.wantCalls)
			}
		})
	}
}

func TestRetry_StopsWhenContextCancelled(t *testing.T) {
	f := newFakeAPI(t)
	f.route("GET", "/api/thing", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusServiceUnavailable) })
	c := f.client(t, func(c *Config) { c.BackoffBase, c.BackoffMax = time.Hour, time.Hour })
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	err := c.get(ctx, "/api/thing", nil, nil)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want deadline exceeded", err)
	}
	if time.Since(start) > 5*time.Second {
		t.Error("did not stop waiting when the context ended")
	}
}

func TestAPIError_Mapping(t *testing.T) {
	f := newFakeAPI(t)
	f.route("GET", "/api/zones", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"success":false,"msg":"You do not have permissions to access this api endpoint"}`))
	})
	f.route("GET", "/api/networks/1", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = w.Write([]byte(`{"success":false,"msg":"Invalid","errors":{"cidr":"is invalid","name":"is required"}}`))
	})
	c := f.client(t)

	err := c.get(t.Context(), "/api/zones", nil, nil)
	if !IsForbidden(err) || IsNotFound(err) {
		t.Fatalf("want forbidden, got %v", err)
	}
	if !strings.Contains(err.Error(), "You do not have permissions") || !strings.Contains(err.Error(), "tenant administrator") {
		t.Errorf("message lacks platform text or hint: %v", err)
	}

	err = c.get(t.Context(), "/api/networks/1", nil, nil)
	want := "Invalid; cidr: is invalid; name: is required"
	if !strings.Contains(err.Error(), want) {
		t.Errorf("got %q, want it to contain %q", err, want)
	}

	if !IsNotFound(c.get(t.Context(), "/api/missing", nil, nil)) {
		t.Error("want not found")
	}
}

func TestPlacement(t *testing.T) {
	f := newFakeAPI(t)
	f.withPlacement()
	c := f.client(t)
	p, err := c.Placement(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if p.CloudID != 4 || p.GroupID != 9101 || p.CloudName != "MTNNG_CLOUD_AZ_1" {
		t.Errorf("got %+v", p)
	}
	if q := f.last("/api/options/clouds").URL.Query().Get("groupId"); q != "9101" {
		t.Errorf("clouds asked for group %q", q)
	}
}

// With no group configured, the caller's only group becomes the default; with several, none does.
func defaultGroup(t *testing.T, c *Client) string {
	t.Helper()
	d, err := c.DefaultsFor(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	return d.Group
}

func TestDefaultsFor_SoleGroup(t *testing.T) {
	one := newFakeAPI(t)
	one.route("GET", "/api/options/groups", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"name":"MTNNG_CLOUD_AZ_1","value":9101}]}`))
	})
	if g := defaultGroup(t, one.client(t)); g != "MTNNG_CLOUD_AZ_1" {
		t.Errorf("sole group: got %q", g)
	}
	if g := defaultGroup(t, one.client(t, func(c *Config) { c.Defaults = Defaults{Group: "mine"} })); g != "mine" {
		t.Errorf("configured group: got %q", g)
	}
	two := newFakeAPI(t)
	two.route("GET", "/api/options/groups", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"name":"one","value":1},{"name":"two","value":2}]}`))
	})
	if g := defaultGroup(t, two.client(t)); g != "" {
		t.Errorf("several groups: got %q, want none", g)
	}
}

func TestPlacement_AmbiguousGroupIsAnError(t *testing.T) {
	f := newFakeAPI(t)
	f.route("GET", "/api/options/groups", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"name":"one","value":1},{"name":"two","value":2}]}`))
	})
	_, err := f.client(t).Placement(t.Context())
	if err == nil || !strings.Contains(err.Error(), "2 groups (one, two)") || !strings.Contains(err.Error(), "MTNCLOUD_GROUP") {
		t.Fatalf("got %v", err)
	}
}

// The provider's group and cloud pick among several (case ignored); a wrong name lists the choices.
func TestPlacement_DefaultsPick(t *testing.T) {
	f := newFakeAPI(t)
	f.route("GET", "/api/options/groups", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"name":"one","value":1},{"name":"two","value":2}]}`))
	})
	f.route("GET", "/api/options/clouds", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"name":"AZ_1","value":4},{"name":"AZ_2","value":5}]}`))
	})
	p, err := f.client(t, func(c *Config) { c.Defaults = Defaults{Group: "TWO", Cloud: "az_2"} }).Placement(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if p.GroupID != 2 || p.CloudID != 5 || p.GroupName != "two" {
		t.Errorf("got %+v", p)
	}
	if q := f.last("/api/options/clouds").URL.Query().Get("groupId"); q != "2" {
		t.Errorf("clouds asked for group %q", q)
	}
	_, err = f.client(t, func(c *Config) { c.Defaults = Defaults{Group: "two"} }).Placement(t.Context())
	if err == nil || !strings.Contains(err.Error(), "2 clouds (AZ_1, AZ_2)") || !strings.Contains(err.Error(), "MTNCLOUD_CLOUD") {
		t.Errorf("no cloud chosen: got %v", err)
	}
	_, err = f.client(t, func(c *Config) { c.Defaults = Defaults{Group: "three"} }).Placement(t.Context())
	if !IsNoMatch(err) || !strings.Contains(err.Error(), `no group named "three"`) {
		t.Errorf("unknown group: got %v", err)
	}
	// Reached through a project lookup, a bad group or cloud is titled as one, not as the project.
	if got := ProjectErrorTitle(err); got != "Unknown group or cloud" {
		t.Errorf("title %q for a bad group", got)
	}
	if got := ProjectErrorTitle(fmt.Errorf("lookup: %w", errors.New(`no project named "x"`))); got != "Unknown project" {
		t.Errorf("title %q for a bad project", got)
	}
}

func TestProjects_SkipsGroupHeaders(t *testing.T) {
	f := newFakeAPI(t)
	f.withPlacement()
	f.fixture("GET", "/api/options/zonePools", "options_zone_pools.json")
	got, err := f.client(t).Projects(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[1].Name != "web-prod-Acme" || got[1].ID != 9201 || got[1].ExternalID != "00000000000000000000000000009201" {
		t.Errorf("got %+v", got)
	}
	if q := f.last("/api/options/zonePools").URL.Query().Get("zoneId"); q != "4" {
		t.Errorf("zoneId = %q", q)
	}
}

func TestSecurityGroups_ScopedToProject(t *testing.T) {
	f := newFakeAPI(t)
	f.withPlacement()
	f.fixture("GET", "/api/options/zoneSecurityGroup", "options_security_groups.json")
	got, err := f.client(t).SecurityGroups(t.Context(), 9201)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Name != "default" || got[0].ID != 9301 || got[0].ExternalID != "11111111-1111-4111-8111-111111111111" {
		t.Errorf("got %+v", got)
	}
	q := f.last("/api/options/zoneSecurityGroup").URL.Query()
	if q.Get("poolId") != "9201" || q.Get("zoneId") != "4" {
		t.Errorf("query = %v", q)
	}
}

func TestDatastoresAndExternalNetworks(t *testing.T) {
	f := newFakeAPI(t)
	f.withPlacement()
	f.fixture("GET", "/api/options/datastores", "options_datastores.json")
	f.fixture("GET", "/api/options/openstackExternalNetworks", "options_external_networks.json")
	c := f.client(t)
	ds, err := c.Datastores(t.Context(), 9201)
	if err != nil || len(ds) != 2 || ds[1].Name != "ENCR_AT_REST" {
		t.Fatalf("datastores %+v, %v", ds, err)
	}
	q := f.last("/api/options/datastores").URL.Query()
	if q.Get("resourcePoolId") != "9201" || q.Get("siteId") != "9101" {
		t.Errorf("datastores query = %v", q)
	}
	nets, err := c.ExternalNetworks(t.Context())
	if err != nil || len(nets) != 2 || nets[0].ID != 34 || nets[0].ExternalID != "" {
		t.Fatalf("external networks %+v, %v", nets, err)
	}
}

func TestInstanceTypes_PicksOpenStackLayoutAndPages(t *testing.T) {
	f := newFakeAPI(t)
	body, _ := os.ReadFile(filepath.Join("testdata", "lookups", "instance_types.json"))
	f.route("GET", "/api/instance-types", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("offset") != "0" {
			_, _ = w.Write([]byte(`{"instanceTypes":[]}`))
			return
		}
		_, _ = w.Write(body)
	})
	got, err := f.client(t).InstanceTypes(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[0].Code != "MTN-U24.04LTS" || got[0].LayoutID != 309 || got[2].Category != "apps" {
		t.Errorf("got %+v", got)
	}
	if q := f.last("/api/instance-types").URL.Query().Get("max"); q != "200" {
		t.Errorf("max = %q", q)
	}
}

func TestListAll_ReadsEveryPage(t *testing.T) {
	f := newFakeAPI(t)
	f.route("GET", "/api/catalog/types", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("offset") == "0" {
			items := make([]string, pageSize)
			for i := range items {
				items[i] = `{"id":1,"name":"x"}`
			}
			_, _ = w.Write([]byte(`{"catalogItemTypes":[` + strings.Join(items, ",") + `]}`))
			return
		}
		_, _ = w.Write([]byte(`{"catalogItemTypes":[{"id":2,"name":"last"}]}`))
	})
	got, err := f.client(t).CatalogItems(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != pageSize+1 || got[pageSize].Name != "last" {
		t.Errorf("got %d items", len(got))
	}
}

func TestPlans_ScopedToProjectAndLayout(t *testing.T) {
	f := newFakeAPI(t)
	f.withPlacement()
	f.fixture("GET", "/api/instances/service-plans", "service_plans.json")
	got, err := f.client(t).Plans(t.Context(), 9201, 309)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[1].Name != "G2S4" || got[1].Cores != 2 || got[1].MemoryBytes != 4<<30 || got[1].RootBytes != 10<<30 {
		t.Errorf("got %+v", got)
	}
	q := f.last("/api/instances/service-plans").URL.Query()
	if q.Get("resourcePoolId") != "9201" || q.Get("layoutId") != "309" || q.Get("zoneId") != "4" {
		t.Errorf("query = %v", q)
	}
}

func TestCache_ConcurrentCallersShareOneRequest(t *testing.T) {
	f := newFakeAPI(t)
	f.withPlacement()
	release := make(chan struct{})
	body, _ := os.ReadFile(filepath.Join("testdata", "lookups", "options_zone_pools.json"))
	f.route("GET", "/api/options/zonePools", func(w http.ResponseWriter, _ *http.Request) {
		<-release
		_, _ = w.Write(body)
	})
	c := f.client(t)
	var wg sync.WaitGroup
	errs := make(chan error, 50)
	for range 50 {
		wg.Go(func() {
			_, err := c.Projects(t.Context())
			errs <- err
		})
	}
	time.Sleep(50 * time.Millisecond)
	close(release)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if n := f.count("GET", "/api/options/zonePools"); n != 1 {
		t.Errorf("zonePools called %d times, want 1", n)
	}
}

func TestCache_FailureIsNotCached(t *testing.T) {
	f := newFakeAPI(t)
	f.withPlacement()
	var n atomic.Int32
	f.route("GET", "/api/options/openstackExternalNetworks", func(w http.ResponseWriter, _ *http.Request) {
		if n.Add(1) == 1 {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		_, _ = w.Write([]byte(`{"data":[{"name":"public-network-01","value":34}]}`))
	})
	c := f.client(t)
	if _, err := c.ExternalNetworks(t.Context()); err == nil {
		t.Fatal("want first call to fail")
	}
	got, err := c.ExternalNetworks(t.Context())
	if err != nil || len(got) != 1 {
		t.Fatalf("second call: %+v, %v", got, err)
	}
}

func TestLogger_NeverSeesCredentials(t *testing.T) {
	f := newFakeAPI(t)
	f.route("POST", "/oauth/token", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"access_token":"secret-token-value"}`)) // ggignore: fake test credential
	})
	f.fixture("GET", "/api/whoami", "whoami_subtenant.json")
	var mu sync.Mutex
	var logged strings.Builder
	c := f.client(t, func(c *Config) {
		c.Token, c.Username, c.Password = "", "jdoe", "secret-password" // ggignore: fake test credential
		c.Logger = func(_ context.Context, msg string, fields map[string]any) {
			mu.Lock()
			defer mu.Unlock()
			logged.WriteString(msg)
			for k, v := range fields {
				fmt.Fprintf(&logged, " %s=%v", k, v)
			}
		}
	})
	if _, err := c.WhoAmI(t.Context()); err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"secret-token-value", "secret-password", "jdoe"} { // ggignore: fake test credential
		if strings.Contains(logged.String(), secret) {
			t.Errorf("log contains %q", secret)
		}
	}
	if !strings.Contains(logged.String(), "path=/api/whoami") {
		t.Errorf("log lacks the request: %s", logged.String())
	}
}

func TestProjectByName(t *testing.T) {
	f := newFakeAPI(t)
	f.withPlacement()
	f.fixture("GET", "/api/whoami", "whoami_subtenant.json")
	f.fixture("GET", "/api/options/zonePools", "options_zone_pools.json")
	c := f.client(t)
	for _, name := range []string{"web-prod", "web-prod-Acme"} {
		p, err := c.ProjectByName(t.Context(), name)
		if err != nil || p.ID != 9201 {
			t.Errorf("%q: got %+v, %v", name, p, err)
		}
	}
	_, err := c.ProjectByName(t.Context(), "Web-Prod")
	if err == nil || !strings.Contains(err.Error(), `no project named "Web-Prod". Your projects: admin, data, web-prod`) {
		t.Errorf("case-sensitive miss: %v", err)
	}
}

func TestProjectByName_Ambiguous(t *testing.T) {
	f := newFakeAPI(t)
	f.withPlacement()
	f.fixture("GET", "/api/whoami", "whoami_subtenant.json")
	f.route("GET", "/api/options/zonePools", func(w http.ResponseWriter, _ *http.Request) {
		// A shared pool called "web" and the tenant's own "web-Acme" both answer to "web".
		_, _ = w.Write([]byte(`{"data":[{"id":1,"value":"pool-1","name":"web"},{"id":2,"value":"pool-2","name":"web-Acme"}]}`))
	})
	_, err := f.client(t).ProjectByName(t.Context(), "web")
	if err == nil || !strings.Contains(err.Error(), "ambiguous, it matches web, web-Acme: use the full name") {
		t.Errorf("got %v", err)
	}
}

func TestImageByCode(t *testing.T) {
	f := newFakeAPI(t)
	f.route("GET", "/api/instance-types", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("offset") != "0" {
			_, _ = w.Write([]byte(`{"instanceTypes":[]}`))
			return
		}
		_, _ = w.Write([]byte(`{"instanceTypes":[
			{"id":89,"code":"MTN-U24.04LTS","category":"os","instanceTypeLayouts":[{"id":309,"provisionTypeCode":"openstack"}]},
			{"id":90,"code":"NO-LAYOUT","category":"os","instanceTypeLayouts":[]},
			{"id":152,"code":"MK8S-M","category":"apps","instanceTypeLayouts":[{"id":386,"provisionTypeCode":"openstack"}]}]}`))
	})
	c := f.client(t)
	if it, err := c.ImageByCode(t.Context(), "MTN-U24.04LTS"); err != nil || it.LayoutID != 309 {
		t.Errorf("got %+v, %v", it, err)
	}
	if _, err := c.ImageByCode(t.Context(), "NO-LAYOUT"); err == nil || !strings.Contains(err.Error(), "has no layout MTN Cloud can provision") {
		t.Errorf("no layout: %v", err)
	}
	// App types resolve (catalog orders need them), but only OS images are suggested.
	if _, err := c.ImageByCode(t.Context(), "MK8S-M"); err != nil {
		t.Errorf("app type: %v", err)
	}
	if _, err := c.ImageByCode(t.Context(), "nope"); err == nil || !strings.Contains(err.Error(), "Available images: MTN-U24.04LTS, NO-LAYOUT") {
		t.Errorf("unknown: %v", err)
	}
}

func TestRawOption_GroupHeaderIsNeverAnItem(t *testing.T) {
	id := int64(7)
	if _, ok := (rawOption{ID: &id, Name: "Resource Pools", IsGroup: true}).option(); ok {
		t.Error("a group header with an id was returned as an item")
	}
	if o, ok := (rawOption{ID: &id, Name: "web", Value: "pool-7"}).option(); !ok || o.ExternalID != "" {
		t.Errorf("pool option: %+v, %v (pool-N is not an external ID)", o, ok)
	}
}

func TestCache_FailureIsSharedByWaiters(t *testing.T) {
	f := newFakeAPI(t)
	release := make(chan struct{})
	f.route("GET", "/api/options/groups", func(w http.ResponseWriter, _ *http.Request) {
		<-release
		w.WriteHeader(http.StatusForbidden)
	})
	c := f.client(t)
	var wg sync.WaitGroup
	for range 50 {
		wg.Go(func() {
			if _, err := c.Placement(t.Context()); !IsForbidden(err) {
				t.Errorf("want forbidden, got %v", err)
			}
		})
	}
	time.Sleep(50 * time.Millisecond)
	close(release)
	wg.Wait()
	if n := f.count("GET", "/api/options/groups"); n != 1 {
		t.Errorf("failing endpoint called %d times, want 1 (waiters share the error)", n)
	}
}

func TestCache_PanicDoesNotHangWaiters(t *testing.T) {
	var lc lookupCache
	_, err := cached(t.Context(), &lc, "k", func(context.Context) (int, error) { panic("boom") })
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("got %v", err)
	}
	v, err := cached(t.Context(), &lc, "k", func(context.Context) (int, error) { return 7, nil })
	if err != nil || v != 7 {
		t.Fatalf("retry after panic: %v, %v", v, err)
	}
}

func TestListAll_FollowsMetaTotalWhenServerCapsMax(t *testing.T) {
	f := newFakeAPI(t)
	f.route("GET", "/api/catalog/types", func(w http.ResponseWriter, r *http.Request) {
		// The server ignores max=200 and returns 2 per page.
		switch r.URL.Query().Get("offset") {
		case "0":
			_, _ = w.Write([]byte(`{"catalogItemTypes":[{"id":1,"name":"a"},{"id":2,"name":"b"}],"meta":{"total":3}}`))
		default:
			_, _ = w.Write([]byte(`{"catalogItemTypes":[{"id":3,"name":"c"}],"meta":{"total":3}}`))
		}
	})
	got, err := f.client(t).CatalogItems(t.Context())
	if err != nil || len(got) != 3 {
		t.Fatalf("got %d items, %v; want 3", len(got), err)
	}
}

func TestListAll_MissingListKeyIsAnError(t *testing.T) {
	f := newFakeAPI(t)
	f.route("GET", "/api/catalog/types", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"renamedKey":[],"meta":{"total":0}}`))
	})
	if _, err := f.client(t).CatalogItems(t.Context()); err == nil || !strings.Contains(err.Error(), `no "catalogItemTypes" list`) {
		t.Fatalf("got %v", err)
	}
}

func TestRawOption_NumericStringValue(t *testing.T) {
	if o, ok := (rawOption{Name: "MTNNG_CLOUD_AZ_1", Value: "4"}).option(); !ok || o.ID != 4 {
		t.Errorf("got %+v, %v", o, ok)
	}
	if _, ok := (rawOption{Name: "x", Value: "not-a-number"}).option(); ok {
		t.Error("a non-numeric value without an id must be dropped")
	}
}

func TestTransportError_HidesQueryString(t *testing.T) {
	f := newFakeAPI(t)
	f.srv.Close() // connection refused
	c := f.client(t, func(c *Config) { c.MaxAttempts = 1 })
	err := c.get(t.Context(), "/api/options/zonePools", url.Values{"secretish": {"web-prod"}}, nil)
	if err == nil || strings.Contains(err.Error(), "secretish") || strings.Contains(err.Error(), "web-prod") {
		t.Fatalf("error leaks the query string: %v", err)
	}
}

func TestDo_SuccessFalseIsAnError(t *testing.T) {
	f := newFakeAPI(t)
	f.route("PUT", "/api/networks/1", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"success":false,"msg":"Gateway is not in the network range"}`))
	})
	err := f.client(t).UpdateNetwork(t.Context(), 1, NetworkInput{Name: "n"})
	if err == nil || !strings.Contains(err.Error(), "request refused: Gateway is not in the network range") {
		t.Fatalf("got %v", err)
	}
	if MayHaveSucceeded(err) {
		t.Error("a refusal was not applied")
	}
}

// A token file is read at the first call and again on a 401, so a token refreshed in the file
// mid-run is used; one still holding the refused token says so.
func TestTokenFile_ReloadOn401(t *testing.T) {
	f := newFakeAPI(t)
	file := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(file, []byte("old\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var refused atomic.Int32
	f.route("GET", "/api/whoami", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer new" {
			if refused.Add(1) == 1 {
				_ = os.WriteFile(file, []byte("new"), 0o600) // refreshed by another process
			}
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(`{"user":{"account":{"id":1,"name":"Acme"}}}`))
	})
	c := f.client(t, func(c *Config) { c.Token, c.TokenFile = "", file })
	if _, err := c.WhoAmI(t.Context()); err != nil {
		t.Fatal(err)
	}
	if refused.Load() != 1 {
		t.Errorf("refused %d times, want 1", refused.Load())
	}

	stale := newFakeAPI(t)
	stale.route("GET", "/api/whoami", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusUnauthorized) })
	c = stale.client(t, func(c *Config) { c.Token, c.TokenFile = "", file })
	if _, err := c.WhoAmI(t.Context()); err == nil || !strings.Contains(err.Error(), "refused the access token in") {
		t.Errorf("err = %v, want the file's token refused", err)
	}
}

// A wait rides out an outage: reads MTN Cloud doesn't answer (503 after the client's own retries, a
// refused connection) don't end a wait that has a deadline, so the apply never reports a cluster it
// couldn't see settle (QA cluster 146). A wait that ends during the outage returns the platform's
// error, not a timeout. An answered error, or a wait without a deadline, still ends at once.
func TestPoll_RidesOutAnOutage(t *testing.T) {
	for _, tc := range []struct {
		name     string
		status   int // during the outage; 0 refuses the connection
		down     int32
		deadline time.Duration // 0: none
		wantErr  string        // "" for success
		maxReads int32         // 0: unchecked
	}{
		{"503s, then ok", http.StatusServiceUnavailable, 8, time.Minute, "", 0},
		{"refused, then ok", 0, 12, time.Minute, "", 0},
		{"down past the deadline: the API's error", http.StatusServiceUnavailable, 1 << 30, 300 * time.Millisecond, "503", 0},
		{"no deadline: ends at once", http.StatusServiceUnavailable, 1 << 30, 0, "503", 5},
		{"403 ends the wait", http.StatusForbidden, 8, time.Minute, "403", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeAPI(t)
			var n atomic.Int32
			f.route("GET", "/api/clusters/7", func(w http.ResponseWriter, _ *http.Request) {
				if n.Add(1) <= tc.down {
					if tc.status == 0 {
						if hj, ok := w.(http.Hijacker); ok {
							conn, _, _ := hj.Hijack()
							_ = conn.Close()
							return
						}
					}
					w.WriteHeader(tc.status)
					_, _ = w.Write([]byte(`{}`))
					return
				}
				_, _ = w.Write([]byte(`{"cluster":{"id":7,"status":"ok"}}`))
			})
			f.route("GET", "/api/clusters/7/workers", func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(`{"workers":[{"id":70,"name":"w","status":"provisioned"}]}`))
			})
			c := f.client(t, func(cfg *Config) { cfg.PollMin, cfg.PollMax = time.Millisecond, 2*time.Millisecond })
			ctx := t.Context()
			if tc.deadline > 0 {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, tc.deadline)
				defer cancel()
			}
			h, err := c.WaitForClusterHealthy(ctx, 7, 1, true)
			switch {
			case tc.wantErr == "" && err != nil:
				t.Fatalf("err = %v", err)
			case tc.wantErr == "" && !h.Healthy(1):
				t.Errorf("not healthy: %+v", h)
			case tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr) || errors.Is(err, context.DeadlineExceeded)):
				t.Errorf("err = %v, want the API's %s", err, tc.wantErr)
			}
			if got := n.Load(); tc.maxReads > 0 && got > tc.maxReads {
				t.Errorf("%d reads, want at most %d", got, tc.maxReads)
			}
		})
	}
}

// A cluster create refused by a label race (QA, 2026-11-03) is sent again only once a lookup by name
// shows nothing was made, so a retry can never make a second cluster.
func TestCreateCluster_LabelRace(t *testing.T) {
	const race = `{"success":false,"msg":"Hibernate operation: could not execute statement; SQL [n/a]; ` +
		`Duplicate entry '72-tf-acc' for key 'label.UK33de5cd9712c08acd93649417773'"}`
	for _, tc := range []struct {
		name      string
		existing  string // the lookup's answer
		wantPosts int
		wantErr   bool
	}{
		{"nothing made: sent again", `{"clusters":[],"meta":{"total":0}}`, 2, false},
		{"one made after all: not sent again", `{"clusters":[{"id":9,"name":"k8s"}],"meta":{"total":1}}`, 1, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeAPI(t)
			var posts atomic.Int32
			f.route("POST", "/api/clusters", func(w http.ResponseWriter, _ *http.Request) {
				if posts.Add(1) == 1 {
					w.WriteHeader(http.StatusBadRequest)
					_, _ = w.Write([]byte(race))
					return
				}
				_, _ = w.Write([]byte(`{"success":true,"cluster":{"id":9}}`))
			})
			f.route("GET", "/api/clusters", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(tc.existing)) })
			id, err := f.client(t).CreateCluster(t.Context(), map[string]any{"name": "k8s"})
			if (err != nil) != tc.wantErr || (!tc.wantErr && id != 9) {
				t.Fatalf("id %d, err %v", id, err)
			}
			if got := int(posts.Load()); got != tc.wantPosts {
				t.Errorf("%d creates sent, want %d", got, tc.wantPosts)
			}
		})
	}
}

// A cluster is healthy only when it is "ok" with every worker ready, and the count asked for. Cluster 143
// (2026-11-03) said "error" with every worker "provisioned"; a cluster stays "ok" while a worker is added.
func TestClusterHealth(t *testing.T) {
	ws := func(st ...string) []ClusterNode {
		var out []ClusterNode
		for i, s := range st {
			out = append(out, ClusterNode{ID: int64(i), Name: fmt.Sprint("w", i), Status: s})
		}
		return out
	}
	for _, tc := range []struct {
		name            string
		status          string
		workers         []ClusterNode
		want            int
		healthy, failed bool
	}{
		{"ok, all ready", "ok", ws("provisioned", "provisioned"), 2, true, false},
		{"ok, any count", "ok", ws("provisioned"), -1, true, false},
		{"ok, one still building", "ok", ws("provisioned", "provisioning"), 2, false, false},
		{"ok, one short", "ok", ws("provisioned"), 2, false, false},
		{"still provisioning", "provisioning", ws("provisioned"), 1, false, false},
		{"error with every worker ready", "error", ws("provisioned"), 1, false, true},
		{"a worker failed", "ok", ws("provisioned", "failed"), 2, false, true},
		{"warning", "warning", ws("provisioned"), 1, false, false},
	} {
		h := &ClusterHealth{Cluster: &Cluster{ID: 1, Status: tc.status}, Workers: tc.workers}
		if got := h.Healthy(tc.want); got != tc.healthy {
			t.Errorf("%s: healthy %v, want %v", tc.name, got, tc.healthy)
		}
		if got := h.Failed(); got != tc.failed {
			t.Errorf("%s: failed %v, want %v", tc.name, got, tc.failed)
		}
	}
	var none *ClusterHealth
	if none.Failed() || none.Healthy(-1) {
		t.Error("a cluster never read is neither failed nor healthy")
	}
}

// After the create, a 404 is read again; later, it means the cluster is gone and the wait ends at once.
func TestWaitForClusterHealthy_NotFound(t *testing.T) {
	f := newFakeAPI(t)
	var n atomic.Int32
	f.route("GET", "/api/clusters/7", func(w http.ResponseWriter, _ *http.Request) {
		if n.Add(1) <= 2 {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{}`))
			return
		}
		_, _ = w.Write([]byte(`{"cluster":{"id":7,"status":"ok"}}`))
	})
	f.route("GET", "/api/clusters/7/workers", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"workers":[]}`))
	})
	c := f.client(t, func(cfg *Config) { cfg.PollMin, cfg.PollMax = time.Millisecond, 2*time.Millisecond })
	if h, err := c.WaitForClusterHealthy(t.Context(), 7, 0, true); err != nil || !h.Healthy(0) {
		t.Fatalf("just created: %+v, %v", h, err)
	}
	n.Store(0)
	if _, err := c.WaitForClusterHealthy(t.Context(), 7, 0, false); !IsNotFound(err) || n.Load() != 1 {
		t.Errorf("later: err %v after %d reads, want a 404 at once", err, n.Load())
	}
}

// A cluster destroy force-deletes it with its infrastructure and associated instances (owner,
// 2026-11-03), as the CLI's `clusters remove --remove-resources on --remove-instances on -f` sends it.
func TestDeleteCluster_Forced(t *testing.T) {
	f := newFakeAPI(t)
	var q string
	f.route("DELETE", "/api/clusters/7", func(w http.ResponseWriter, r *http.Request) {
		q = r.URL.RawQuery
		_, _ = w.Write([]byte(`{"success":true}`))
	})
	if err := f.client(t).DeleteCluster(t.Context(), 7); err != nil {
		t.Fatal(err)
	}
	if q != "force=on&removeInstances=on&removeResources=on" {
		t.Errorf("query %q", q)
	}
}
