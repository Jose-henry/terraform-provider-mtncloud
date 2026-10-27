package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"sync/atomic"
	"testing"
	"time"
)

func instanceFixture(t *testing.T, file string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "instances", file))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestCreateInstance_Body(t *testing.T) {
	f := newFakeAPI(t)
	var body map[string]any
	f.route("POST", "/api/instances", captureBody(t, &body, http.StatusOK, []byte(`{"success":true,"instance":{"id":9701}}`)))

	id, err := f.client(t).CreateInstance(t.Context(), InstanceInput{
		Placement: Placement{GroupID: 9101, CloudID: 4}, ProjectID: 9201, ImageCode: "MTN-U24.04LTS", LayoutID: 309, PlanID: 9402,
		Environment: "production", Labels: []string{"web"}, Tags: map[string]string{"team": "payments"},
		EnvVars: map[string]string{"APP_ENV": "prod"}, NetworkDomainID: 16, SecurityGroupIDs: []int64{9301, 9302},
		Volumes:    []VolumeSpec{{Root: true, Name: "root", Size: 40}, {Name: "data-1", Size: 100, DatastoreID: "2"}},
		NetworkIDs: []int64{9301, 9302}, CreateUser: true,
	})
	if err != nil || id != 9701 {
		t.Fatalf("id %d, err %v", id, err)
	}
	in := body["instance"].(map[string]any)
	ref := func(m map[string]any, k string) any { return m[k].(map[string]any)["id"] }
	// The shape of the CLI's dry run and FastCreateLinuxInstance.sh (2026-10-27).
	if body["zoneId"] != 4.0 || ref(in, "site") != 9101.0 || ref(in, "layout") != 309.0 || ref(in, "plan") != 9402.0 ||
		in["instanceType"].(map[string]any)["code"] != "MTN-U24.04LTS" || in["instanceContext"] != "production" ||
		ref(in, "networkDomain") != 16.0 || body["copies"] != 1.0 || body["layoutSize"] != 1.0 {
		t.Errorf("body %v", body)
	}
	// No name or hostname: the tenant's naming policy names the instance.
	if _, ok := in["name"]; ok {
		t.Errorf("name sent without one configured: %v", in["name"])
	}
	if _, ok := in["hostName"]; ok {
		t.Errorf("hostName sent without one configured")
	}
	cfg := body["config"].(map[string]any)
	if cfg["resourcePoolId"] != "pool-9201" || cfg["createUser"] != true || cfg["noAgent"] != false {
		t.Errorf("config %v", cfg)
	}
	vols := body["volumes"].([]any)
	root, data := vols[0].(map[string]any), vols[1].(map[string]any)
	if root["id"] != -1.0 || root["rootVolume"] != true || root["size"] != 40.0 || root["datastoreId"] != "auto" || root["storageType"] != 11.0 ||
		data["rootVolume"] != false || data["datastoreId"] != "2" || data["name"] != "data-1" {
		t.Errorf("volumes %v", vols)
	}
	nics := body["networkInterfaces"].([]any)
	if n0 := nics[0].(map[string]any); n0["primaryInterface"] != true || ref(n0, "network") != "network-9301" {
		t.Errorf("primary nic %v", n0)
	}
	if n1 := nics[1].(map[string]any); n1["primaryInterface"] != false || ref(n1, "network") != "network-9302" {
		t.Errorf("second nic %v", n1)
	}
	if sgs := body["securityGroups"].([]any); len(sgs) != 2 || sgs[0].(map[string]any)["id"] != 9301.0 {
		t.Errorf("security groups %v", sgs)
	}
	if tags := body["tags"].([]any); len(tags) != 1 || tags[0].(map[string]any)["name"] != "team" {
		t.Errorf("tags %v", tags)
	}
	if ev := body["evars"].([]any); len(ev) != 1 || ev[0].(map[string]any)["value"] != "prod" {
		t.Errorf("evars %v", ev)
	}
}

func TestCreateInstance_RefusesMissingPieces(t *testing.T) {
	f := newFakeAPI(t)
	if _, err := f.client(t).CreateInstance(t.Context(), InstanceInput{ProjectID: 9201, LayoutID: 309, PlanID: 9402}); err == nil {
		t.Fatal("want an error: an instance without a network can't be created")
	}
	if n := f.count("POST", "/api/instances"); n != 0 {
		t.Errorf("%d POSTs", n)
	}
}

func TestGetInstance_Reads(t *testing.T) {
	f := newFakeAPI(t)
	body := instanceFixture(t, "get.json")
	f.route("GET", "/api/instances/9701", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(body) })
	in, err := f.client(t).GetInstance(t.Context(), 9701)
	if err != nil {
		t.Fatal(err)
	}
	if in.ProjectID != 9201 || in.PlanName != "G2S4" || in.ImageName != "MTN Ubuntu Server 24.04.3LTS" || in.Environment != "production" ||
		in.NetworkDomainID != 16 || in.Tags["team"] != "payments" || in.NoAgent || in.LayoutSize != 1 {
		t.Errorf("%+v", in)
	}
	// The private IP is the platform's <PREFIX>_IP variable; the floating IP is the other address.
	if in.PrivateIP != "10.20.1.15" || in.PublicIP != "203.0.113.20" {
		t.Errorf("ips %q %q", in.PrivateIP, in.PublicIP)
	}
	// Root and primary come first whatever order the API uses.
	if !in.Volumes[0].Root || in.Volumes[0].Size != 40 || in.Volumes[1].DatastoreID != "2" {
		t.Errorf("volumes %+v", in.Volumes)
	}
	if in.Interfaces[0].NetworkID != 9301 || in.Interfaces[0].ID != "9820" || in.Interfaces[1].NetworkID != 9302 {
		t.Errorf("interfaces %+v", in.Interfaces)
	}
	if !in.IsPlatformEnvVar("MTN_UBUNTU_SERVER_24_04_3LTS_PORT_SSH") || in.IsPlatformEnvVar("APP_ENV") {
		t.Error("platform variables are told apart by the instance's prefix")
	}
}

func TestGetInstance_NoFloatingIP(t *testing.T) {
	f := newFakeAPI(t)
	body := bytes.Replace(instanceFixture(t, "get.json"), []byte(`"203.0.113.20"`), []byte(`"10.20.1.15"`), 1)
	f.route("GET", "/api/instances/9701", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(body) })
	in, err := f.client(t).GetInstance(t.Context(), 9701)
	if err != nil {
		t.Fatal(err)
	}
	if in.PublicIP != "" {
		t.Errorf("public IP %q: connection info with the private address is not a floating IP", in.PublicIP)
	}
}

func TestFindInstances_ExactNameInProject(t *testing.T) {
	f := newFakeAPI(t)
	f.route("GET", "/api/instances", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"instances":[
			{"id":1,"name":"app-1","config":{"resourcePoolId":"pool-9201"}},
			{"id":2,"name":"app-1","config":{"resourcePoolId":"pool-9202"}},
			{"id":3,"name":"app-10","config":{"resourcePoolId":"pool-9201"}}],"meta":{"total":3}}`))
	})
	got, err := f.client(t).FindInstances(t.Context(), "app-1", 9201)
	if err != nil || len(got) != 1 || got[0].ID != 1 {
		t.Fatalf("%v %v", got, err)
	}
	if q := f.last("/api/instances").URL.Query().Get("name"); q != "app-1" {
		t.Errorf("name filter %q", q)
	}
}

func TestUpdateInstance_Body(t *testing.T) {
	f := newFakeAPI(t)
	var body map[string]any
	f.route("PUT", "/api/instances/9701", captureBody(t, &body, http.StatusOK, []byte(`{"success":true}`)))
	err := f.client(t).UpdateInstance(t.Context(), 9701, InstanceUpdate{Name: "app-2", Environment: "dev", Tags: map[string]string{}})
	if err != nil {
		t.Fatal(err)
	}
	in := body["instance"].(map[string]any)
	// Tags and labels go as full (here empty) lists, so removing the last one works.
	if in["name"] != "app-2" || in["instanceContext"] != "dev" || len(in["tags"].([]any)) != 0 || len(in["labels"].([]any)) != 0 {
		t.Errorf("body %v", in)
	}
}

func TestResizeInstance_KeepsExistingAndAddsNew(t *testing.T) {
	f := newFakeAPI(t)
	var body map[string]any
	f.route("PUT", "/api/instances/9701/resize", captureBody(t, &body, http.StatusOK, []byte(`{"success":true}`)))
	err := f.client(t).ResizeInstance(t.Context(), 9701, ResizeInput{
		PlanID:     9403,
		Volumes:    []VolumeSpec{{ID: 9810, Root: true, Name: "root", Size: 40}, {Size: 50}},
		Interfaces: []Interface{{ID: "9820", NetworkID: 9301}, {NetworkID: 9303}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if body["instance"].(map[string]any)["plan"].(map[string]any)["id"] != 9403.0 || body["deleteOriginalVolumes"] != false {
		t.Errorf("body %v", body)
	}
	vols := body["volumes"].([]any)
	if vols[0].(map[string]any)["id"] != 9810.0 || vols[1].(map[string]any)["id"] != -1.0 {
		t.Errorf("volumes %v", vols)
	}
	nics := body["networkInterfaces"].([]any)
	if nics[0].(map[string]any)["id"] != "9820" || nics[1].(map[string]any)["id"] != nil {
		t.Errorf("nics %v", nics)
	}
}

func TestSetInstanceSecurityGroups_SortedIDs(t *testing.T) {
	f := newFakeAPI(t)
	var body map[string]any
	f.route("POST", "/api/instances/9701/security-groups", captureBody(t, &body, http.StatusOK, []byte(`{"success":true}`)))
	if err := f.client(t).SetInstanceSecurityGroups(t.Context(), 9701, []int64{9302, 9301}); err != nil {
		t.Fatal(err)
	}
	if ids := body["securityGroupIds"].([]any); !slices.Equal(ids, []any{9301.0, 9302.0}) {
		t.Errorf("ids %v", ids)
	}
}

// instanceSeq answers GET /api/instances/9701 with each status in turn, then the last one.
func instanceSeq(t *testing.T, f *fakeAPI, statuses ...string) {
	var n atomic.Int32
	base := instanceFixture(t, "get.json")
	f.route("GET", "/api/instances/9701", func(w http.ResponseWriter, _ *http.Request) {
		i := min(int(n.Add(1))-1, len(statuses)-1)
		switch statuses[i] {
		case "404":
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"success":false,"msg":"Instance not found"}`))
		default:
			_, _ = w.Write(bytes.Replace(base, []byte(`"status": "running"`), []byte(`"status": "`+statuses[i]+`"`), 1))
		}
	})
}

func TestWaitForInstance(t *testing.T) {
	for _, tc := range []struct {
		name     string
		statuses []string
		want     string
	}{
		{"provisioning then running", []string{"provisioning", "provisioning", "running"}, "running"},
		{"404 just after the create", []string{"404", "provisioning", "running"}, "running"},
		{"resizing then running", []string{"resizing", "running"}, "running"},
		{"failed", []string{"provisioning", "failed"}, "failed"},
		{"error", []string{"provisioning", "error"}, "error"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeAPI(t)
			instanceSeq(t, f, tc.statuses...)
			in, err := f.client(t, fastPoll).WaitForInstance(t.Context(), 9701)
			if err != nil || in.Status != tc.want {
				t.Fatalf("status %v, err %v", in, err)
			}
		})
	}
}

func TestWaitForInstance_TimeoutKeepsLastStatus(t *testing.T) {
	f := newFakeAPI(t)
	instanceSeq(t, f, "provisioning")
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	in, err := f.client(t, fastPoll).WaitForInstance(ctx, 9701)
	if !errors.Is(err, context.DeadlineExceeded) || in == nil || in.Status != "provisioning" {
		t.Fatalf("in %v, err %v", in, err)
	}
}

func TestWaitForInstanceGone(t *testing.T) {
	for _, tc := range [][]string{{"running", "404"}, {"running", "pendingRemoval"}} {
		f := newFakeAPI(t)
		instanceSeq(t, f, tc...)
		if err := f.client(t, fastPoll).WaitForInstanceGone(t.Context(), 9701); err != nil {
			t.Errorf("%v: %v", tc, err)
		}
	}
}

func TestDeleteInstance_GoneIsDone(t *testing.T) {
	f := newFakeAPI(t)
	f.route("DELETE", "/api/instances/9701", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"success":false,"msg":"Instance not found"}`))
	})
	if err := f.client(t).DeleteInstance(t.Context(), 9701); err != nil {
		t.Fatal(err)
	}
}

func TestInstanceLookups(t *testing.T) {
	f := newFakeAPI(t)
	f.withPlacement()
	f.fixture("GET", "/api/whoami", "whoami_subtenant.json")
	f.fixture("GET", "/api/options/zonePools", "options_zone_pools.json")
	f.fixture("GET", "/api/options/zoneSecurityGroup", "options_security_groups.json")
	f.fixture("GET", "/api/options/datastores", "options_datastores.json")
	f.fixture("GET", "/api/instances/service-plans", "service_plans.json")
	f.fixture("GET", "/api/instance-types", "instance_types.json")
	f.route("GET", "/api/options/environments", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"success":true,"data":[{"name":"Dev","value":"dev"},{"name":"Production","value":"production"}]}`))
	})
	f.route("GET", "/api/networks/domains", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"networkDomains":[{"id":16,"name":"example.com"}],"meta":{"total":1}}`))
	})
	c := f.client(t)
	ctx := t.Context()
	p, err := c.PlacementForGroup(ctx, "mtnng_cloud_az_1")
	if err != nil || p.GroupID != 9101 || p.CloudID != 4 {
		t.Fatalf("placement %+v %v", p, err)
	}
	if _, err := c.PlacementForGroup(ctx, "other"); err == nil {
		t.Error("an unknown group is an error")
	}
	project, err := c.ProjectByNameAt(ctx, p, "web-prod")
	if err != nil || project.ID != 9201 {
		t.Fatalf("project %+v %v", project, err)
	}
	img, err := c.ImageByName(ctx, "mtn ubuntu server 24.04.3lts")
	if err != nil || img.LayoutID != 309 {
		t.Fatalf("image %+v %v", img, err)
	}
	if byCode, err := c.ImageByName(ctx, "MTN-U24.04LTS"); err != nil || byCode.ID != img.ID {
		t.Errorf("image by code %+v %v", byCode, err)
	}
	if plan, err := c.PlanByName(ctx, p, 9201, 309, "g2s4"); err != nil || plan.ID != 9402 {
		t.Errorf("plan %+v %v", plan, err)
	}
	if sg, err := c.SecurityGroupByName(ctx, p, 9201, "WEB"); err != nil || sg.ID != 9302 {
		t.Errorf("security group %+v %v", sg, err)
	}
	if def, ok, err := c.DefaultSecurityGroup(ctx, p, 9201); err != nil || !ok || def.ID != 9301 {
		t.Errorf("default group %+v %v %v", def, ok, err)
	}
	if ds, err := c.DatastoreByName(ctx, p, 9201, "encr_at_rest"); err != nil || ds.ID != 414 {
		t.Errorf("datastore %+v %v", ds, err)
	}
	for _, name := range []string{"Production", "production"} {
		if env, err := c.EnvironmentByName(ctx, name); err != nil || env.Value != "production" {
			t.Errorf("environment %q: %+v %v", name, env, err)
		}
	}
	if d, err := c.NetworkDomainByName(ctx, "Example.com."); err != nil || d.ID != 16 {
		t.Errorf("domain %+v %v", d, err)
	}
	for _, name := range []string{"default", "Acme-Default-SG"} {
		if !IsDefaultSecurityGroup(name) {
			t.Errorf("%q is a default group name", name)
		}
	}
	if IsDefaultSecurityGroup("web") {
		t.Error("web is not a default group")
	}
}

// IDs come as numbers or strings depending on the call; a big number must not turn into "1.2e+06".
func TestInstance_NumericIDs(t *testing.T) {
	var w wireInstance
	body := `{"id":1,"volumes":[{"id":1,"name":"root","size":10,"rootVolume":true,"datastoreId":1234567}],
		"interfaces":[{"id":2345678,"network":{"id":9301},"primaryInterface":true},{"id":null,"network":{"id":9302}}]}`
	if err := json.Unmarshal([]byte(body), &w); err != nil {
		t.Fatal(err)
	}
	in := w.instance()
	if got := in.Volumes[0].DatastoreID; got != "1234567" {
		t.Errorf("datastore %q, want 1234567", got)
	}
	if got := in.Interfaces[0].ID; got != "2345678" {
		t.Errorf("interface %q, want 2345678", got)
	}
	if got := in.Interfaces[1].ID; got != "" {
		t.Errorf("null interface ID read as %q, want empty", got)
	}
}

func TestInstanceThreshold_Read(t *testing.T) {
	f := newFakeAPI(t)
	for name, file := range map[string]string{"unset": "threshold_unset.json", "set": "threshold_set.json"} {
		body := instanceFixture(t, file)
		f.route("GET", "/api/instances/9701/threshold", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(body) })
		th, err := f.client(t).InstanceThreshold(t.Context(), 9701)
		if err != nil {
			t.Fatal(err)
		}
		switch name {
		case "unset":
			if th.Set() || th.MinCount != 1 || th.MaxCount != 0 {
				t.Errorf("unset: %+v", th)
			}
		case "set":
			// A disabled metric keeps its old marks in the API; it reads as not watched.
			if !th.Set() || th.MaxCount != 3 || th.CPU == nil || th.CPU.Max != 50.5 || th.Memory != nil || th.Disk != nil {
				t.Errorf("set: %+v", th)
			}
		}
	}
}

func TestSetInstanceThreshold_Body(t *testing.T) {
	f := newFakeAPI(t)
	var body map[string]any
	f.route("PUT", "/api/instances/9701/threshold", captureBody(t, &body, http.StatusOK, []byte(`{"success":true}`)))
	c := f.client(t)
	err := c.SetInstanceThreshold(t.Context(), 9701, Threshold{AutoUp: true, MinCount: 1, MaxCount: 3, Increment: 1, Memory: &Range{Min: 20, Max: 80}})
	if err != nil {
		t.Fatal(err)
	}
	th := body["instanceThreshold"].(map[string]any)
	if th["autoUp"] != true || th["maxCount"] != 3.0 || th["memoryEnabled"] != true || th["maxMemory"] != 80.0 ||
		th["cpuEnabled"] != false || th["diskEnabled"] != false {
		t.Errorf("body %v", th)
	}
	// Off: everything disabled, counts back to the platform's defaults.
	if err := c.SetInstanceThreshold(t.Context(), 9701, Threshold{}); err != nil {
		t.Fatal(err)
	}
	th = body["instanceThreshold"].(map[string]any)
	if th["autoUp"] != false || th["minCount"] != 1.0 || th["scaleIncrement"] != 1.0 || th["maxCount"] != nil || th["cpuEnabled"] != false {
		t.Errorf("off body %v", th)
	}
}

func TestCreateInstance_Lifecycle(t *testing.T) {
	f := newFakeAPI(t)
	var body map[string]any
	f.route("POST", "/api/instances", captureBody(t, &body, http.StatusOK, []byte(`{"instance":{"id":9701}}`)))
	in := InstanceInput{ProjectID: 9201, ImageCode: "MTN-U24.04LTS", LayoutID: 1, PlanID: 9402, NetworkIDs: []int64{9301}, ExpireDays: 30}
	if _, err := f.client(t).CreateInstance(t.Context(), in); err != nil {
		t.Fatal(err)
	}
	inst := body["instance"].(map[string]any)
	if _, ok := inst["shutdownDays"]; ok || inst["expireDays"] != 30.0 {
		t.Errorf("instance %v", inst)
	}
}
