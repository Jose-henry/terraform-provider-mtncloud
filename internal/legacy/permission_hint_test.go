package legacy

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	sdk "github.com/gomorpheus/morpheus-go-sdk"
	"github.com/hashicorp/terraform-plugin-sdk/v2/diag"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
)

func TestPermissionHint(t *testing.T) {
	for _, tc := range []struct {
		status   int
		wantHint bool
	}{
		{http.StatusForbidden, true},
		{http.StatusNotFound, false},
		{http.StatusInternalServerError, false},
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(tc.status)
			_, _ = w.Write([]byte(`{"success":false,"msg":"refused"}`))
		}))
		read := func(ctx context.Context, d *schema.ResourceData, meta any) diag.Diagnostics {
			_, err := meta.(*sdk.Client).Get(&sdk.Request{Path: "/api/things/1"})
			return diag.FromErr(err)
		}
		r := &schema.Resource{ReadContext: read, Schema: map[string]*schema.Schema{}}
		withPermissionHints(&schema.Provider{DataSourcesMap: map[string]*schema.Resource{"mtncloud_thing": r}})

		c := sdk.NewClient(srv.URL)
		c.SetAccessToken("test-token", "", 3600, "write")
		diags := r.ReadContext(t.Context(), r.TestResourceData(), c)
		srv.Close()

		if !diags.HasError() {
			t.Fatalf("%d: no error", tc.status)
		}
		got := strings.Contains(diags[0].Detail, "Your role does not allow this call")
		if got != tc.wantHint {
			t.Errorf("%d: hint=%v, want %v (detail %q)", tc.status, got, tc.wantHint, diags[0].Detail)
		}
		if diags[0].Summary != "refused" {
			t.Errorf("%d: summary %q, want the platform's message", tc.status, diags[0].Summary)
		}
		if c.RequestCount() != 0 {
			t.Errorf("%d: the shared client made %d requests, want 0 (calls use a copy)", tc.status, c.RequestCount())
		}
	}
}

// Every legacy data source is wrapped: any read that reached the server and got a 403 says so.
func TestPermissionHint_AllWrapped(t *testing.T) {
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"success":false,"msg":"refused"}`))
	}))
	defer srv.Close()
	reached := 0
	for name, r := range Provider().DataSourcesMap {
		c := sdk.NewClient(srv.URL)
		c.SetAccessToken("test-token", "", 3600, "write")
		d := r.TestResourceData()
		_ = d.Set("name", "x")
		before := hits.Load()
		diags := r.ReadContext(t.Context(), d, c)
		if hits.Load() == before || !diags.HasError() {
			continue
		}
		reached++
		hinted := false
		for _, dg := range diags {
			hinted = hinted || strings.Contains(dg.Detail, "Your role does not allow this call")
		}
		if !hinted {
			t.Errorf("%s: a 403 without the hint: %v", name, diags)
		}
	}
	if reached == 0 {
		t.Fatal("no data source reached the server")
	}
	t.Logf("%d data sources reached the server", reached)
}

// Importers get a copy of the client too, so an import never writes to the shared one.
func TestPermissionHint_ImporterUsesCopy(t *testing.T) {
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"cyphers":[{"id":7,"itemKey":"secret/app/db"}]}`))
	}))
	defer srv.Close()
	r := Provider().ResourcesMap["mtncloud_cypher_secret"]
	c := sdk.NewClient(srv.URL)
	c.SetAccessToken("test-token", "", 3600, "write")
	d := r.TestResourceData()
	d.SetId("7")
	out, err := r.Importer.StateContext(t.Context(), d, c)
	if err != nil {
		t.Fatal(err)
	}
	if hits.Load() == 0 {
		t.Fatal("the importer never reached the server")
	}
	if c.RequestCount() != 0 {
		t.Errorf("the shared client made %d requests, want 0 (imports use a copy)", c.RequestCount())
	}
	if got := out[0].Get("key"); got != "app/db" {
		t.Errorf("key %q, want app/db", got)
	}
}

// A failed login on the shared client isn't tried again by every call: each call logs in once itself.
func TestPermissionHint_FailedLoginTriedOnce(t *testing.T) {
	var logins atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/oauth/token") {
			logins.Add(1)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"invalid_grant","error_description":"Bad credentials"}`))
	}))
	defer srv.Close()
	read := func(ctx context.Context, d *schema.ResourceData, meta any) diag.Diagnostics {
		_, err := meta.(*sdk.Client).Get(&sdk.Request{Path: "/api/things/1"})
		return diag.FromErr(err)
	}
	r := &schema.Resource{ReadContext: read, Schema: map[string]*schema.Schema{}}
	withPermissionHints(&schema.Provider{DataSourcesMap: map[string]*schema.Resource{"mtncloud_thing": r}})
	c := sdk.NewClient(srv.URL)
	c.Username, c.Password = "user", "wrong"
	const calls = 3
	for range calls {
		if diags := r.ReadContext(t.Context(), r.TestResourceData(), c); !diags.HasError() {
			t.Fatal("no error for a failed login")
		}
	}
	// One shared attempt, then one per call.
	if n := logins.Load(); n != calls+1 {
		t.Errorf("%d login attempts for %d calls, want %d", n, calls, calls+1)
	}
}

func TestListAllVirtualImages_Pages(t *testing.T) {
	const total = 5
	var offsets []string
	failAt := ""
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		off := r.URL.Query().Get("offset")
		offsets = append(offsets, off)
		if off == failAt {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"success":false,"msg":"boom"}`))
			return
		}
		o, _ := strconv.Atoi(off)
		m, _ := strconv.Atoi(r.URL.Query().Get("max"))
		var items []string
		for i := o; i < total && i < o+m; i++ {
			items = append(items, fmt.Sprintf(`{"id":%d,"name":"img-%d"}`, i+1, i+1))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"virtualImages":[%s],"meta":{"total":%d}}`, strings.Join(items, ","), total)
	}))
	defer srv.Close()
	c := sdk.NewClient(srv.URL)
	c.SetAccessToken("test-token", "", 3600, "write")

	images, err := ListAllVirtualImages(c, 2, "asc", "all")
	if err != nil {
		t.Fatal(err)
	}
	if len(images) != total || images[total-1].ID != total {
		t.Errorf("got %d images, want %d ending with ID %d", len(images), total, total)
	}
	if got := strings.Join(offsets, ","); got != "0,2,4" {
		t.Errorf("offsets %s, want 0,2,4", got)
	}

	offsets, failAt = nil, "2"
	if _, err := ListAllVirtualImages(c, 2, "asc", "all"); err == nil {
		t.Error("no error when page 2 fails")
	}
}
