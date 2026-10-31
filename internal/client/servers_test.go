package client

import (
	"context"
	"net/http"
	"slices"
	"testing"
)

func TestFillServerIPs(t *testing.T) {
	f := newFakeAPI(t)
	f.route("GET", "/api/containers/71", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"container":{"id":71,"ip":"203.0.113.71","internalIp":"10.0.0.71"}}`))
	})
	f.route("GET", "/api/containers/72", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"container":{"id":72,"ip":"10.0.0.72","internalIp":"10.0.0.72"}}`)) // no floating IP
	})
	c := f.client(t)
	ctx := context.Background()

	one := &Instance{ContainerIDs: []int64{71}, PublicIP: "203.0.113.71"}
	if err := c.FillServerIPs(ctx, one); err != nil || !slices.Equal(one.PublicIPs, []string{"203.0.113.71"}) {
		t.Errorf("one server: %v %v, want its address and no call", one.PublicIPs, err)
	}
	if n := f.count("GET", "/api/containers/71"); n != 0 {
		t.Errorf("one server read its container %d times, want 0", n)
	}

	two := &Instance{ContainerIDs: []int64{71, 72}}
	if err := c.FillServerIPs(ctx, two); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(two.PublicIPs, []string{"203.0.113.71", ""}) || !two.MissingPublicIP() || two.NoPublicIP() {
		t.Errorf("two servers: %v (missing %v, none %v), want the first's address and the second missing",
			two.PublicIPs, two.MissingPublicIP(), two.NoPublicIP())
	}
	if two.PublicIP != "203.0.113.71" {
		t.Errorf("PublicIP %q, want a server's address", two.PublicIP)
	}

	// connectionInfo showing another server's private address: the servers decide.
	stale := &Instance{ContainerIDs: []int64{72, 72}, PublicIP: "10.0.0.99"}
	if err := c.FillServerIPs(ctx, stale); err != nil || !stale.NoPublicIP() || !stale.MissingPublicIP() {
		t.Errorf("stale connectionInfo: %v %v (none %v), want no address on any server", stale.PublicIPs, err, stale.NoPublicIP())
	}

	// Any other refusal is an error, not a fallback.
	f.route("GET", "/api/containers/74", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"success":false,"msg":"bad"}`))
	})
	if err := c.FillServerIPs(ctx, &Instance{ContainerIDs: []int64{71, 74}}); err == nil {
		t.Error("a refused server read returned no error")
	}

	// A role that can't read servers: the instance's one address stands, no error.
	f.route("GET", "/api/containers/73", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"success":false,"msg":"Forbidden"}`))
	})
	denied := &Instance{ContainerIDs: []int64{71, 73}, PublicIP: "203.0.113.71"}
	if err := c.FillServerIPs(ctx, denied); err != nil || denied.PublicIPs != nil || denied.MissingPublicIP() {
		t.Errorf("forbidden: %v %v, want the instance's one address and no error", denied.PublicIPs, err)
	}
}
