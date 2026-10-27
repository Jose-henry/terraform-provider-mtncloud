package client

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"sync/atomic"
	"testing"
	"time"
)

func routerFixture(t *testing.T, file string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "routers", file))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// fastPoll keeps waits in tests to milliseconds.
func fastPoll(c *Config) { c.PollMin, c.PollMax = time.Millisecond, 5*time.Millisecond }

func TestCreateRouter_Body(t *testing.T) {
	f := newFakeAPI(t)
	f.withPlacement()
	f.fixture("GET", "/api/network-router-types", "router_types.json")
	var body map[string]any
	f.route("POST", "/api/networks/routers", captureBody(t, &body, http.StatusOK, []byte(`{"success":true,"id":9601}`)))

	id, err := f.client(t).CreateRouter(t.Context(), RouterInput{
		Name: "edge", Description: "Edge router", ProjectID: 9201, Enabled: true, ExternalNetworkID: 32, SubnetIDs: []int64{9311, 9312},
	})
	if err != nil || id != 9601 {
		t.Fatalf("id %d, err %v", id, err)
	}
	b := body["networkRouter"].(map[string]any)
	ref := func(k string) any { return b[k].(map[string]any)["id"] }
	// Captured working body (owner-approved live run, 2026-10-27).
	if b["name"] != "edge" || ref("type") != 1.0 || ref("site") != 9101.0 || ref("zone") != 4.0 || b["poolId"] != 9201.0 ||
		ref("externalNetwork") != 32.0 || b["enabled"] != true {
		t.Errorf("body %v", b)
	}
	ids := b["interfaces"].(map[string]any)["networkSubnet"].(map[string]any)["id"].([]any)
	if !slices.Equal(ids, []any{9311.0, 9312.0}) {
		t.Errorf("interfaces %v", ids)
	}
}

func TestCreateRouter_RefusesNoProject(t *testing.T) {
	f := newFakeAPI(t)
	if _, err := f.client(t).CreateRouter(t.Context(), RouterInput{Name: "edge"}); err == nil {
		t.Fatal("want an error: without a project the platform picks one")
	}
	if n := f.count("POST", "/api/networks/routers"); n != 0 {
		t.Errorf("%d POSTs", n)
	}
}

func TestGetRouter_SplitsGatewayFromNetworks(t *testing.T) {
	f := newFakeAPI(t)
	f.fixture("GET", "/api/networks/routers/9601", filepath.Join("..", "routers", "get.json"))
	r, err := f.client(t).GetRouter(t.Context(), 9601)
	if err != nil {
		t.Fatal(err)
	}
	if r.ExternalNetworkName != "public-network-02" || r.ExternalIP != "203.0.113.10" || !slices.Equal(r.NetworkIDs, []int64{9301}) ||
		r.ExternalID == "" || r.Status != "ok" || r.Description != "Edge router" {
		t.Errorf("%+v", r)
	}
}

func TestGetRouter_NoGatewayMeansNoExternalIP(t *testing.T) {
	// After the gateway is removed the API still sends the old externalIp (captured 2026-10-27).
	f := newFakeAPI(t)
	f.fixture("GET", "/api/networks/routers/9601", filepath.Join("..", "routers", "no_gateway.json"))
	r, err := f.client(t).GetRouter(t.Context(), 9601)
	if err != nil {
		t.Fatal(err)
	}
	if r.ExternalNetworkID != 0 || r.ExternalIP != "" || !slices.Equal(r.NetworkIDs, []int64{9301}) {
		t.Errorf("%+v", r)
	}
}

func TestUpdateRouter_Body(t *testing.T) {
	f := newFakeAPI(t)
	var body map[string]any
	f.route("PUT", "/api/networks/routers/9601", captureBody(t, &body, http.StatusOK, []byte(`{"success":true}`)))
	c := f.client(t)
	if err := c.UpdateRouter(t.Context(), 9601, RouterInput{Name: "renamed", Description: "x", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	b := body["networkRouter"].(map[string]any)
	if _, ok := b["name"]; ok {
		t.Error("update must not send the name: the API refuses it")
	}
	if v, ok := b["externalNetwork"]; !ok || v != nil {
		t.Errorf("no gateway must send externalNetwork: null, got %v (present %v)", v, ok)
	}
	if ids := b["interfaces"].(map[string]any)["networkSubnet"].(map[string]any)["id"].([]any); len(ids) != 0 {
		t.Errorf("no networks must send an empty list, got %v", ids)
	}
	body = nil // decoding merges into an existing map
	if err := c.UpdateRouter(t.Context(), 9601, RouterInput{ExternalNetworkID: 32, KeepGateway: true}); err != nil {
		t.Fatal(err)
	}
	if _, ok := body["networkRouter"].(map[string]any)["externalNetwork"]; ok {
		t.Error("an unchanged gateway must not be sent again")
	}
	for _, k := range []string{"poolId", "zone", "site", "type"} {
		if _, ok := b[k]; ok {
			t.Errorf("update must not send %q", k)
		}
	}
}

func TestWaitForRouter(t *testing.T) {
	for name, tc := range map[string]struct {
		replies []string // file names, or "404"
		status  string
	}{
		"provisioning then ok":  {[]string{"provisioning.json", "provisioning.json", "get.json"}, "ok"},
		"404 just after create": {[]string{"404", "get.json"}, "ok"},
		"failed is returned":    {[]string{"provisioning.json", "failed"}, "failed"},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFakeAPI(t)
			var n atomic.Int32
			f.route("GET", "/api/networks/routers/9601", func(w http.ResponseWriter, _ *http.Request) {
				i := min(int(n.Add(1))-1, len(tc.replies)-1)
				switch tc.replies[i] {
				case "404":
					w.WriteHeader(http.StatusNotFound)
					_, _ = w.Write([]byte(`{"success":false,"msg":"Network Router not found"}`))
				case "failed":
					_, _ = w.Write([]byte(`{"networkRouter":{"id":9601,"name":"edge","status":"failed"}}`))
				default:
					_, _ = w.Write(routerFixture(t, tc.replies[i]))
				}
			})
			r, err := f.client(t, fastPoll).WaitForRouter(t.Context(), 9601)
			if err != nil || r.Status != tc.status {
				t.Fatalf("got %+v, %v", r, err)
			}
		})
	}
}

func TestWaitForRouter_StopsAtTimeout(t *testing.T) {
	f := newFakeAPI(t)
	f.route("GET", "/api/networks/routers/9601", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(routerFixture(t, "provisioning.json"))
	})
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	r, err := f.client(t, fastPoll).WaitForRouter(ctx, 9601)
	if err == nil || r == nil || r.Status != "provisioning" {
		t.Fatalf("want a timeout with the last status, got %+v, %v", r, err)
	}
}

func TestWaitForRouterGone(t *testing.T) {
	f := newFakeAPI(t)
	var n atomic.Int32
	f.route("GET", "/api/networks/routers/9601", func(w http.ResponseWriter, _ *http.Request) {
		if n.Add(1) < 3 {
			_, _ = w.Write(routerFixture(t, "get.json"))
			return
		}
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"success":false}`))
	})
	if err := f.client(t, fastPoll).WaitForRouterGone(t.Context(), 9601); err != nil {
		t.Fatal(err)
	}
	if n.Load() != 3 {
		t.Errorf("%d reads, want 3", n.Load())
	}
}

func TestDeleteRouter_GoneIsDone(t *testing.T) {
	f := newFakeAPI(t)
	f.route("DELETE", "/api/networks/routers/9601", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"success":false}`))
	})
	if err := f.client(t).DeleteRouter(t.Context(), 9601); err != nil {
		t.Fatal(err)
	}
}

func TestFindRouters_ExactName(t *testing.T) {
	f := newFakeAPI(t)
	f.route("GET", "/api/networks/routers", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"networkRouters":[{"id":1,"name":"edge-2"},{"id":9601,"name":"edge"}],"meta":{"total":2}}`))
	})
	c := f.client(t)
	if rs, err := c.FindRouters(t.Context(), "edge"); err != nil || len(rs) != 1 || rs[0].ID != 9601 {
		t.Errorf("got %+v, %v", rs, err)
	}
	if rs, err := c.FindRouters(t.Context(), "edg"); err != nil || len(rs) != 0 {
		t.Errorf("prefix matched: %+v, %v", rs, err)
	}
}

func TestWaitForRouter_TimeoutDuringReadKeepsLastStatus(t *testing.T) {
	f := newFakeAPI(t)
	var n atomic.Int32
	f.route("GET", "/api/networks/routers/9601", func(w http.ResponseWriter, r *http.Request) {
		if n.Add(1) > 1 {
			<-r.Context().Done() // the deadline lands while this read is in flight
			return
		}
		_, _ = w.Write(routerFixture(t, "provisioning.json"))
	})
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	r, err := f.client(t, fastPoll).WaitForRouter(ctx, 9601)
	if !errors.Is(err, context.DeadlineExceeded) || r == nil || r.Status != "provisioning" {
		t.Fatalf("want the deadline and the last status, got %+v, %v", r, err)
	}
}

func TestSend_RateLimitPastDeadlineIsTheDeadline(t *testing.T) {
	f := newFakeAPI(t)
	f.fixture("GET", "/api/options/groups", "options_groups.json")
	// One request per hour: the second can't start before the deadline, so the limiter refuses it.
	c := f.client(t, func(c *Config) { c.RequestsPerSec, c.Burst = 1.0/3600, 1 })
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	if _, err := c.options(ctx, "groups", nil); err != nil {
		t.Fatal(err)
	}
	_, err := c.options(ctx, "groups", url.Values{"x": {"1"}})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("want context.DeadlineExceeded, got %v", err)
	}
}
