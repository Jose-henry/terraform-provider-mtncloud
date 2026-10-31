package client

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func netFixture(t *testing.T, file string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "networks", file))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func (f *fakeAPI) withNetworkPlacement() {
	f.withPlacement()
	f.fixture("GET", "/api/options/creatableNetworkTypes", "options_network_types.json")
	f.fixture("GET", "/api/options/networkServer", "options_network_servers.json")
}

func TestCreateNetwork_Body(t *testing.T) {
	f := newFakeAPI(t)
	f.withNetworkPlacement()
	var body map[string]any
	f.route("POST", "/api/networks", captureBody(t, &body, http.StatusOK, netFixture(t, "get.json")))

	n, err := f.client(t).CreateNetwork(t.Context(), NetworkInput{
		Name: "app-net", Description: "application tier", ProjectID: 9201, CIDR: "10.20.1.0/24", Gateway: "10.20.1.1",
		DNSPrimary: "102.88.134.41", DNSSecondary: "102.90.109.125", DHCP: true, AllowIPOverride: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	b := body["network"].(map[string]any)
	ref := func(k string) any { return b[k].(map[string]any)["id"] }
	if ref("zonePool") != float64(9201) || ref("type") != float64(12) || ref("networkServer") != float64(4) || ref("site") == nil {
		t.Errorf("placement in body = %v", b)
	}
	if b["cidr"] != "10.20.1.0/24" || b["gateway"] != "10.20.1.1" || b["ipv4Enabled"] != true || b["ipv6Enabled"] != false ||
		b["visibility"] != "private" || b["active"] != true || b["dhcpServer"] != true || b["allowStaticOverride"] != true {
		t.Errorf("body = %v", b)
	}
	if _, ok := b["cidrIPv6"]; ok {
		t.Error("IPv6 fields sent with IPv6 off")
	}
	if n.ID != 9301 || n.SubnetID != 9311 || n.ProjectName != "web-prod-Acme" || n.IPv6 != nil || n.ExternalID == "" {
		t.Errorf("network = %+v", n)
	}
	if _, err := f.client(t).CreateNetwork(t.Context(), NetworkInput{Name: "x"}); err == nil {
		t.Error("a network without a project must be refused, never left to the platform's default")
	}
}

func TestCreateNetwork_IPv6(t *testing.T) {
	f := newFakeAPI(t)
	f.withNetworkPlacement()
	var body map[string]any
	f.route("POST", "/api/networks", captureBody(t, &body, http.StatusOK, netFixture(t, "get.json")))
	_, err := f.client(t).CreateNetwork(t.Context(), NetworkInput{Name: "n", ProjectID: 1, CIDR: "10.0.0.0/24", Gateway: "10.0.0.1",
		IPv6: &NetworkIPv6{CIDR: "2001:db8:1::/64", Gateway: "2001:db8:1::1", DHCP: true}})
	if err != nil {
		t.Fatal(err)
	}
	b := body["network"].(map[string]any)
	if b["ipv6Enabled"] != true || b["cidrIPv6"] != "2001:db8:1::/64" || b["gatewayIPv6"] != "2001:db8:1::1" || b["dhcpServerIPv6"] != true {
		t.Errorf("body = %v", b)
	}
}

func TestNetworkPlacement_RefusesToGuess(t *testing.T) {
	f := newFakeAPI(t)
	f.withPlacement()
	f.fixture("GET", "/api/options/creatableNetworkTypes", "options_network_types.json")
	f.route("GET", "/api/options/networkServer", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("networkType.id") != "12" {
			t.Errorf("query = %s", r.URL.RawQuery)
		}
		_, _ = w.Write([]byte(`{"success":true,"data":[{"name":"A","value":1},{"name":"B","value":4}]}`))
	})
	if _, err := f.client(t).NetworkPlacement(t.Context()); err == nil || !strings.Contains(err.Error(), "network service") {
		t.Errorf("got %v", err)
	}
}

func TestUpdateNetwork_Body(t *testing.T) {
	f := newFakeAPI(t)
	var body map[string]any
	f.route("PUT", "/api/networks/9301", captureBody(t, &body, http.StatusOK, []byte(`{"success":true}`)))
	if err := f.client(t).UpdateNetwork(t.Context(), 9301, NetworkInput{Name: "n", Gateway: "10.0.0.254", CIDR: "10.9.9.0/24", ProjectID: 5}); err != nil {
		t.Fatal(err)
	}
	b := body["network"].(map[string]any)
	if b["gateway"] != "10.0.0.254" {
		t.Errorf("body = %v", b)
	}
	for _, k := range []string{"cidr", "zonePool", "type", "site"} {
		if _, ok := b[k]; ok {
			t.Errorf("update must not send %q", k)
		}
	}
}

func TestDeleteNetwork_InUse(t *testing.T) {
	f := newFakeAPI(t)
	// Captured 2026-10-27: the refusal comes back as 200 with "success": false.
	f.route("DELETE", "/api/networks/9301", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(netFixture(t, "in_use.json"))
	})
	f.route("DELETE", "/api/networks/9302", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"success":false,"msg":"Network not found"}`))
	})
	c := f.client(t)
	if err := c.DeleteNetwork(t.Context(), 9301); !IsInUse(err) {
		t.Errorf("want in use, got %v", err)
	}
	if err := c.DeleteNetwork(t.Context(), 9302); err != nil {
		t.Errorf("already gone: %v", err)
	}
	if IsInUse(&APIError{StatusCode: 400, Message: "invalid name"}) {
		t.Error("other errors are not in use")
	}
}

func TestFindNetwork_MatchesNameAndProject(t *testing.T) {
	f := newFakeAPI(t)
	f.route("GET", "/api/networks", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"networks":[
			{"id":1,"name":"app-net","zonePool":{"id":9202,"name":"data-Acme"}},
			{"id":2,"name":"app-net","zonePool":{"id":9201,"name":"web-prod-Acme"}},
			{"id":3,"name":"app-net-2","zonePool":{"id":9201,"name":"web-prod-Acme"}}],"meta":{"total":3}}`))
	})
	if n, err := f.client(t).FindNetwork(t.Context(), "app-net", 9201); err != nil || n == nil || n.ID != 2 {
		t.Errorf("got %+v, %v", n, err)
	}
}

// A network made moments ago gets its subnet a little later: the wait reads until it has one.
func TestWaitForNetworkSubnet(t *testing.T) {
	f := newFakeAPI(t)
	reads := 0
	f.route("GET", "/api/networks/690", func(w http.ResponseWriter, _ *http.Request) {
		reads++
		subnets := `[]`
		if reads > 2 {
			subnets = `[{"id":700,"name":"net subnet","cidr":"10.0.0.0/24"}]`
		}
		_, _ = w.Write([]byte(`{"network":{"id":690,"name":"net","zonePool":{"id":1},"subnets":` + subnets + `}}`))
	})
	n, err := f.client(t, fastPoll).WaitForNetworkSubnet(t.Context(), 690)
	if err != nil || n.SubnetID != 700 || reads != 3 {
		t.Fatalf("got %+v, %v after %d reads; want subnet 700 on the third read", n, err, reads)
	}
}
