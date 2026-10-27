package provider

import (
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/knownvalue"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/statecheck"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"
)

// instStore is a stateful fake of /api/instances that behaves like the captures (2026-10-27): a
// new instance reads "provisioning" once before "running"; the naming template names an instance
// sent without a name; the platform adds its own <PREFIX>_* variables; groups are kept per instance.
type instStore struct {
	mu           sync.Mutex
	nextID, seq  int64
	insts        map[int64]map[string]any
	sgs          map[int64][]int64
	thresholds   map[int64]map[string]any
	thresholdErr bool // PUT /threshold answers 400
	fipErr       bool // attach answers 400, as for a network with no router
	fip504       bool // attach is applied but answers 504, so only the wait can tell
	fipSilent    bool // attach answers 200 but no address ever shows
	detachStuck  bool // detach answers 200 but the address stays
	attachCalls  int
	attached     []int64
	pending      map[int64]bool
	fail         string // the next create ends in this status
	stuck        bool   // instances never leave "provisioning"
	lostReplies  int    // the next creates are applied but answer 502
	creates      int
	lastCreate   map[string]any
	resizes      []map[string]any
}

func newInstStore() *instStore {
	return &instStore{nextID: 9700, insts: map[int64]map[string]any{}, sgs: map[int64][]int64{}, thresholds: map[int64]map[string]any{}, pending: map[int64]bool{}}
}

var (
	fakeImages = map[string]string{"MTN-U24.04LTS": "MTN Ubuntu Server 24.04.3LTS", "MTN-WINSVR2025": "MTN Windows Server 2025 (BYOL)"}
	fakePlans  = map[float64]string{9401: "G1S1", 9402: "G2S4"}
	fakeSGs    = map[int64]string{9301: "default", 9302: "web"}
	nonAlnum   = regexp.MustCompile(`[^A-Za-z0-9]+`)
)

func (s *instStore) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.insts)
}

func (s *instStore) view(id int64) map[string]any {
	out := map[string]any{}
	for k, v := range s.insts[id] {
		out[k] = v
	}
	if s.pending[id] || s.stuck {
		out["status"] = "provisioning"
		delete(s.pending, id)
	}
	return out
}

func (s *instStore) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	reply := func(status int, v any) {
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(v)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if strings.HasPrefix(r.URL.Path, "/api/options/") || strings.HasPrefix(r.URL.Path, "/api/containers/") {
		s.floating(w, r, reply)
		return
	}
	rest := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/instances"), "/")
	if rest == "" {
		switch r.Method {
		case http.MethodGet:
			list := []any{}
			for id, in := range s.insts {
				if name := r.URL.Query().Get("name"); name == "" || in["name"] == name {
					list = append(list, s.view(id))
				}
			}
			reply(http.StatusOK, map[string]any{"instances": list, "meta": map[string]any{"total": len(list)}})
		case http.MethodPost:
			s.create(body, reply)
		}
		return
	}
	parts := strings.SplitN(rest, "/", 2)
	id, _ := strconv.ParseInt(parts[0], 10, 64)
	in := s.insts[id]
	if in == nil {
		reply(http.StatusNotFound, map[string]any{"success": false, "msg": "Instance not found"})
		return
	}
	switch {
	case len(parts) == 2 && parts[1] == "threshold" && r.Method == http.MethodGet:
		th := s.thresholds[id]
		if th == nil { // as captured for an instance never scaled
			th = map[string]any{"id": nil, "autoUp": false, "autoDown": false, "minCount": 1, "maxCount": nil, "scaleIncrement": 1,
				"cpuEnabled": false, "memoryEnabled": false, "diskEnabled": false}
		}
		reply(http.StatusOK, map[string]any{"instance": map[string]any{"id": id}, "instanceThreshold": th, "instanceSchedules": []any{}})
	case len(parts) == 2 && parts[1] == "threshold" && s.thresholdErr:
		reply(http.StatusBadRequest, map[string]any{"success": false, "msg": "Invalid threshold"})
	case len(parts) == 2 && parts[1] == "threshold":
		s.thresholds[id] = body["instanceThreshold"].(map[string]any)
		reply(http.StatusOK, map[string]any{"success": true, "instanceThreshold": s.thresholds[id]})
	case len(parts) == 2 && parts[1] == "security-groups" && r.Method == http.MethodGet:
		list := []any{}
		for _, g := range s.sgs[id] {
			list = append(list, map[string]any{"id": g, "name": fakeSGs[g]})
		}
		reply(http.StatusOK, map[string]any{"success": true, "securityGroups": list})
	case len(parts) == 2 && parts[1] == "security-groups":
		var ids []int64
		for _, v := range body["securityGroupIds"].([]any) {
			ids = append(ids, int64(v.(float64)))
		}
		s.sgs[id] = ids
		reply(http.StatusOK, map[string]any{"success": true})
	case len(parts) == 2 && parts[1] == "resize":
		s.resizes = append(s.resizes, body)
		plan := body["instance"].(map[string]any)["plan"].(map[string]any)["id"].(float64)
		in["plan"] = map[string]any{"id": plan, "name": fakePlans[plan]}
		vols := in["volumes"].([]any)
		for _, v := range body["volumes"].([]any) {
			if v.(map[string]any)["id"] == -1.0 {
				vols = append(vols, s.volume(id, len(vols), v.(map[string]any)))
			}
		}
		in["volumes"] = vols
		nics := in["interfaces"].([]any)
		for _, n := range body["networkInterfaces"].([]any) {
			if _, ok := n.(map[string]any)["id"]; !ok {
				nics = append(nics, s.nic(id, len(nics), n.(map[string]any)))
			}
		}
		in["interfaces"] = nics
		in["status"] = "running" // a reconfigure restarts the VM
		s.pending[id] = true
		reply(http.StatusOK, map[string]any{"success": true})
	case r.Method == http.MethodGet:
		reply(http.StatusOK, map[string]any{"instance": s.view(id)})
	case r.Method == http.MethodPut:
		for k, v := range body["instance"].(map[string]any) {
			in[k] = v
		}
		reply(http.StatusOK, map[string]any{"success": true, "instance": s.view(id)})
	case r.Method == http.MethodDelete:
		delete(s.insts, id)
		reply(http.StatusOK, map[string]any{"success": true})
	}
}

func (s *instStore) volume(id int64, i int, v map[string]any) map[string]any {
	name, _ := v["name"].(string)
	if name == "" {
		name = fmt.Sprintf("data-%d", i)
	}
	return map[string]any{"id": id*10 + int64(i), "name": name, "size": v["size"], "rootVolume": v["rootVolume"],
		"storageType": 11, "datastoreId": v["datastoreId"]}
}

func (s *instStore) nic(id int64, i int, n map[string]any) map[string]any {
	net, _ := strconv.ParseInt(strings.TrimPrefix(n["network"].(map[string]any)["id"].(string), "network-"), 10, 64)
	return map[string]any{"id": strconv.FormatInt(id*100+int64(i), 10), "network": map[string]any{"id": net},
		"ipAddress": nil, "primaryInterface": i == 0}
}

func (s *instStore) create(body map[string]any, reply func(int, any)) {
	s.creates++
	s.lastCreate = body
	req := body["instance"].(map[string]any)
	s.nextID++
	s.seq++
	id := s.nextID
	code := req["instanceType"].(map[string]any)["code"].(string)
	name, _ := req["name"].(string)
	if name == "" { // the tenant's naming template
		name = fmt.Sprintf("jd-9001-MTN-CLOUD-%s-%d", code, s.seq)
	}
	host, _ := req["hostName"].(string)
	if host == "" {
		host = strings.ToLower(nonAlnum.ReplaceAllString(name, "-"))
	}
	prefix := strings.ToUpper(nonAlnum.ReplaceAllString(fakeImages[code], "_"))
	ip := fmt.Sprintf("10.20.1.%d", 10+id%200)
	evars := []any{
		map[string]any{"name": prefix + "_IP", "value": ip}, map[string]any{"name": prefix + "_HOST", "value": "container" + strconv.FormatInt(id, 10)},
		map[string]any{"name": prefix + "_PORT_SSH", "value": 22},
	}
	if ev, ok := body["evars"].([]any); ok {
		evars = append(ev, evars...)
	}
	plan := req["plan"].(map[string]any)["id"].(float64)
	cfg := body["config"].(map[string]any)
	in := map[string]any{
		"id": id, "name": name, "hostName": host, "status": "running", "environmentPrefix": prefix,
		"description": req["description"], "instanceContext": req["instanceContext"], "labels": req["labels"], "tags": body["tags"],
		"group":        map[string]any{"id": req["site"].(map[string]any)["id"], "name": "MTNNG_CLOUD_AZ_1"},
		"instanceType": map[string]any{"code": code, "name": fakeImages[code]},
		"plan":         map[string]any{"id": plan, "name": fakePlans[plan]},
		"config":       map[string]any{"resourcePoolId": cfg["resourcePoolId"], "noAgent": cfg["noAgent"], "createUser": cfg["createUser"], "layoutSize": body["layoutSize"]},
		"evars":        evars, "connectionInfo": []any{map[string]any{"ip": ip, "port": 22, "name": "SSH"}},
		"containers": []any{id*10 + 7},
	}
	if days, ok := req["expireDays"].(float64); ok {
		in["expireDays"], in["expireDate"] = days, "2026-11-26T09:00:00Z"
	}
	if d, ok := req["networkDomain"].(map[string]any); ok {
		in["networkDomain"] = map[string]any{"id": d["id"], "name": "example.com"}
	}
	var vols, nics []any
	for i, v := range body["volumes"].([]any) {
		vols = append(vols, s.volume(id, i, v.(map[string]any)))
	}
	for i, n := range body["networkInterfaces"].([]any) {
		nics = append(nics, s.nic(id, i, n.(map[string]any)))
	}
	in["volumes"], in["interfaces"] = vols, nics
	var sgs []int64
	for _, g := range body["securityGroups"].([]any) {
		sgs = append(sgs, int64(g.(map[string]any)["id"].(float64)))
	}
	s.sgs[id] = sgs
	if s.fail != "" {
		in["status"], in["statusMessage"] = s.fail, "No valid host was found"
		s.fail = ""
	}
	s.insts[id], s.pending[id] = in, true
	if s.lostReplies > 0 {
		s.lostReplies--
		reply(http.StatusBadGateway, map[string]any{"success": false, "msg": "Bad Gateway"})
		return
	}
	reply(http.StatusOK, map[string]any{"success": true, "instance": s.view(id)})
}

func instConfig(f *fakeAPI, attrs string) string {
	return f.providerConfig() + `
resource "mtncloud_instance" "app" {
  group    = "MTNNG_CLOUD_AZ_1"
  project  = "web-prod"
  image    = "MTN Ubuntu Server 24.04.3LTS"
` + attrs + `
}
`
}

func newInstFakes(t *testing.T) (*fakeAPI, *instStore) {
	t.Helper()
	f := newFakeAPI(t)
	s := newInstStore()
	f.inst = s
	return f, s
}

func instPlan(action plancheck.ResourceActionType) resource.ConfigPlanChecks {
	return resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction("mtncloud_instance.app", action)}}
}

func TestInstance_Lifecycle(t *testing.T) {
	f, s := newInstFakes(t)
	lock := func(fn func()) func() { return func() { s.mu.Lock(); defer s.mu.Unlock(); fn() } }
	base := `  plan     = "G1S1"
  networks = ["9301"]
  security_groups = ["web"]
  environment = "Production"
  labels   = ["web"]
  tags     = { team = "payments" }
  env_vars = { APP_ENV = "prod" }
  domain   = "example.com"
  data_volume {
    size      = 50
    datastore = "ENCR_AT_REST"
  }`
	attr := func(p string) tfjsonpath.Path { return tfjsonpath.New(p) }
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories(),
		Steps: []resource.TestStep{
			{
				// No name: the naming template names it, and the name is read back.
				Config: instConfig(f, base),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue("mtncloud_instance.app", attr("name"), knownvalue.StringRegexp(regexp.MustCompile(`^jd-9001-MTN-CLOUD-MTN-U24\.04LTS-\d+$`))),
					statecheck.ExpectKnownValue("mtncloud_instance.app", attr("status"), knownvalue.StringExact("running")),
					statecheck.ExpectKnownValue("mtncloud_instance.app", attr("private_ip"), knownvalue.StringRegexp(regexp.MustCompile(`^10\.20\.1\.`))),
					statecheck.ExpectKnownValue("mtncloud_instance.app", attr("root_volume_size"), knownvalue.Int64Exact(10)),
					// Only the variable the user set: not the platform's <PREFIX>_IP, _HOST, _PORT_*.
					statecheck.ExpectKnownValue("mtncloud_instance.app", attr("env_vars"), knownvalue.MapExact(map[string]knownvalue.Check{"APP_ENV": knownvalue.StringExact("prod")})),
					// The default group is applied, but not shown: only "web" was asked for.
					statecheck.ExpectKnownValue("mtncloud_instance.app", attr("security_groups"), knownvalue.SetExact([]knownvalue.Check{knownvalue.StringExact("web")})),
				},
			},
			{
				PreConfig: lock(func() {
					b := s.lastCreate
					sgs := fmt.Sprint(b["securityGroups"])
					if !strings.Contains(sgs, "9301") || !strings.Contains(sgs, "9302") {
						t.Errorf("security groups sent %s, want the default (9301) and web (9302)", sgs)
					}
					if _, ok := b["instance"].(map[string]any)["name"]; ok {
						t.Error("a name was sent although none is configured")
					}
					cfg := b["config"].(map[string]any)
					if cfg["resourcePoolId"] != "pool-9201" || cfg["noAgent"] != false || cfg["createUser"] != true {
						t.Errorf("config %v", cfg)
					}
					if ds := b["volumes"].([]any)[1].(map[string]any)["datastoreId"]; ds != "414" {
						t.Errorf("data volume datastore %v, want 414 (ENCR_AT_REST)", ds)
					}
				}),
				Config:           instConfig(f, base),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()}},
			},
			{
				ResourceName: "mtncloud_instance.app", ImportState: true, ImportStateVerify: true,
				ImportStateVerifyIgnore: []string{"timeouts"}, Config: instConfig(f, base),
			},
			{
				// In place: rename, new plan (restart), tags, groups, an added disk and network.
				Config: instConfig(f, `  name     = "app-1"
  plan     = "G2S4"
  networks = ["9301", "9303"]
  environment = "Dev"
  labels   = ["web", "blue"]
  tags     = { team = "payments", tier = "1" }
  env_vars = { APP_ENV = "prod" }
  domain   = "example.com"
  data_volume {
    size      = 50
    datastore = "ENCR_AT_REST"
  }
  data_volume {
    size = 20
  }`),
				ConfigPlanChecks: instPlan(plancheck.ResourceActionUpdate),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue("mtncloud_instance.app", attr("name"), knownvalue.StringExact("app-1")),
					statecheck.ExpectKnownValue("mtncloud_instance.app", attr("plan"), knownvalue.StringExact("G2S4")),
					statecheck.ExpectKnownValue("mtncloud_instance.app", attr("security_groups"), knownvalue.Null()),
				},
			},
			{
				// The same things named another way: no change.
				PreConfig: lock(func() {
					if len(s.resizes) != 1 {
						t.Errorf("%d resizes, want 1", len(s.resizes))
					}
					if got := fmt.Sprint(s.sgs[s.nextID]); got != "[9301]" {
						t.Errorf("security groups %s after removing web, want the default only", got)
					}
				}),
				Config: f.providerConfig() + `
resource "mtncloud_instance" "app" {
  group    = "mtnng_cloud_az_1"
  project  = "web-prod-Acme"
  image    = "MTN-U24.04LTS"
  name     = "app-1"
  plan     = "g2s4"
  networks = ["9301", "9303"]
  environment = "dev"
  labels   = ["blue", "web"]
  tags     = { team = "payments", tier = "1" }
  env_vars = { APP_ENV = "prod" }
  domain   = "Example.com"
  data_volume {
    size      = 50
    datastore = "encr_at_rest"
  }
  data_volume {
    size = 20
  }
}
`,
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()}},
			},
			{
				// A new primary network can't be set in place.
				Config: instConfig(f, `  name = "app-1"
  plan = "G2S4"
  networks = ["9303"]`),
				ConfigPlanChecks: instPlan(plancheck.ResourceActionDestroyBeforeCreate),
			},
			{
				// Deleted outside Terraform: planned again.
				PreConfig: lock(func() { clear(s.insts) }),
				Config: instConfig(f, `  name = "app-1"
  plan = "G2S4"
  networks = ["9303"]`),
				PlanOnly:           true,
				ExpectNonEmptyPlan: true,
			},
			{Config: f.providerConfig()},
		},
	})
	if n := s.count(); n != 0 {
		t.Errorf("%d instances left", n)
	}
}

func TestInstance_Refusals(t *testing.T) {
	f, s := newInstFakes(t)
	cfg := func(attrs string) string { return instConfig(f, attrs) }
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories(),
		Steps: []resource.TestStep{
			{Config: cfg(`  plan = "G9S9"
  networks = ["9301"]`), PlanOnly: true, ExpectError: regexp.MustCompile(`(?s)Unknown plan.*G1S1,\s+G2S4`)},
			{Config: cfg(`  plan = "G1S1"
  networks = ["9301"]
  security_groups = ["nope"]`), PlanOnly: true, ExpectError: regexp.MustCompile(`(?s)Unknown security group.*default,\s+web`)},
			{Config: cfg(`  plan = "G1S1"
  networks = ["9301"]
  environment = "Prod"`), PlanOnly: true, ExpectError: regexp.MustCompile(`(?s)Unknown environment.*Production`)},
			{Config: strings.Replace(cfg(`  plan = "G1S1"
  networks = ["9301"]`), "MTN Ubuntu Server 24.04.3LTS", "MTN-WINSVR2025", 1), PlanOnly: true,
				ExpectError: regexp.MustCompile(`(?s)Root volume too small for Windows.*at least 40 GB`)},
			{Config: cfg(`  plan = "G1S1"
  networks = ["9301"]
  root_volume_size = 20`)},
			{Config: cfg(`  plan = "G1S1"
  networks = ["9301"]
  root_volume_size = 40`), ExpectError: regexp.MustCompile(`Root volume can't be resized`)},
			{
				// The name is taken in the project: refused, never adopted.
				Config: cfg(`  plan = "G1S1"
  networks = ["9301"]
  root_volume_size = 20`) + `
resource "mtncloud_instance" "twin" {
  group    = "MTNNG_CLOUD_AZ_1"
  project  = "web-prod"
  image    = "MTN-U24.04LTS"
  name     = mtncloud_instance.app.name
  plan     = "G1S1"
  networks = ["9301"]
}
`,
				ExpectError: regexp.MustCompile(`Instance already exists`),
			},
			{Config: f.providerConfig()},
		},
	})
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.creates != 1 {
		t.Errorf("%d creates, want 1", s.creates)
	}
}

func TestInstance_Failures(t *testing.T) {
	f, s := newInstFakes(t)
	lock := func(fn func()) func() { return func() { s.mu.Lock(); defer s.mu.Unlock(); fn() } }
	named := instConfig(f, `  name = "app-1"
  plan = "G1S1"
  networks = ["9301"]`)
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories(),
		Steps: []resource.TestStep{
			{
				// Failed: the platform's reason is shown, and the instance is kept to be replaced.
				PreConfig:   lock(func() { s.fail = "failed" }),
				Config:      named,
				ExpectError: regexp.MustCompile(`(?s)Instance create failed.*"failed": No valid host was found`),
			},
			{
				PreConfig: lock(func() {
					for _, in := range s.insts {
						in["status"] = "failed" // stays failed
					}
				}),
				Config:           named,
				ConfigPlanChecks: instPlan(plancheck.ResourceActionDestroyBeforeCreate),
			},
			{Config: f.providerConfig()},
			{
				// A lost reply: the instance is found by name and kept, not created twice.
				PreConfig: lock(func() { s.lostReplies = 1 }),
				Config:    named,
			},
			{
				PreConfig: lock(func() {
					if len(s.insts) != 1 {
						t.Errorf("%d instances after a lost reply, want 1", len(s.insts))
					}
					for _, in := range s.insts {
						in["status"] = "error" // failed after create, out of band
					}
				}),
				Config:           named,
				ConfigPlanChecks: instPlan(plancheck.ResourceActionDestroyBeforeCreate),
			},
			{Config: f.providerConfig()},
			{
				// Stuck: the create timeout ends the wait, and the instance is kept in state.
				PreConfig: lock(func() { s.stuck = true }),
				Config: strings.Replace(named, `networks = ["9301"]`, `networks = ["9301"]
  timeouts {
    create = "2s"
  }`, 1),
				ExpectError: regexp.MustCompile(`(?s)Timed out waiting for the instance.*still "provisioning"`),
			},
			{PreConfig: lock(func() { s.stuck = false }), Config: f.providerConfig()},
		},
	})
}

func TestInstance_UpdateEdges(t *testing.T) {
	f, s := newInstFakes(t)
	lock := func(fn func()) func() { return func() { s.mu.Lock(); defer s.mu.Unlock(); fn() } }
	small := instConfig(f, `  name = "app-1"
  plan = "G1S1"
  networks = ["9301"]`)
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories(),
		Steps: []resource.TestStep{
			{Config: small},
			{
				// Stopped in the console: no diff, and a resize that brings it back running applies cleanly.
				PreConfig: lock(func() {
					for _, in := range s.insts {
						in["status"] = "stopped"
					}
				}),
				Config:           small,
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()}},
			},
			{
				Config:           strings.Replace(small, `"G1S1"`, `"G2S4"`, 1),
				ConfigPlanChecks: instPlan(plancheck.ResourceActionUpdate),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue("mtncloud_instance.app", tfjsonpath.New("status"), knownvalue.StringExact("running")),
				},
			},
			{
				// A project known only at apply is assumed to be another one: replace, never an update.
				Config: strings.Replace(strings.Replace(small, `"G1S1"`, `"G2S4"`, 1), `project  = "web-prod"`,
					`project  = terraform_data.p.output`, 1) + `
resource "terraform_data" "p" {
  input = "web-prod"
}
`,
				ConfigPlanChecks: instPlan(plancheck.ResourceActionDestroyBeforeCreate),
			},
			{Config: f.providerConfig()},
		},
	})
}

func TestInstance_ScaleAndLifecycle(t *testing.T) {
	f, s := newInstFakes(t)
	cfg := func(extra string) string {
		return instConfig(f, `  name = "app-1"
  plan = "G1S1"
  networks = ["9301"]
  expire_days = 30
`+extra)
	}
	scaled := cfg(`  scale = {
    min_count = 1
    max_count = 3
    cpu       = { min = 30, max = 75.5 }
  }`)
	attr := func(p string) tfjsonpath.Path { return tfjsonpath.New(p) }
	threshold := func() map[string]any {
		s.mu.Lock()
		defer s.mu.Unlock()
		for _, th := range s.thresholds {
			return th
		}
		return nil
	}
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories(),
		Steps: []resource.TestStep{
			{
				Config: scaled,
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue("mtncloud_instance.app", attr("expire_date"), knownvalue.StringExact("2026-11-26T09:00:00Z")),
					statecheck.ExpectKnownValue("mtncloud_instance.app", attr("scale").AtMapKey("increment"), knownvalue.Int64Exact(1)),
					statecheck.ExpectKnownValue("mtncloud_instance.app", attr("scale").AtMapKey("memory"), knownvalue.Null()),
				},
			},
			{
				PreConfig: func() {
					if th := threshold(); th == nil || th["cpuEnabled"] != true || th["maxCpu"] != 75.5 || th["autoUp"] != true {
						t.Errorf("threshold sent %v", th)
					}
				},
				Config:           scaled,
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()}},
			},
			{ResourceName: "mtncloud_instance.app", ImportState: true, ImportStateVerify: true,
				ImportStateVerifyIgnore: []string{"timeouts", "expire_days"}, Config: scaled},
			{
				// Changed in place: memory instead of cpu.
				Config: cfg(`  scale = {
    min_count = 2
    max_count = 4
    memory    = { min = 20, max = 80 }
  }`),
				ConfigPlanChecks: instPlan(plancheck.ResourceActionUpdate),
			},
			{
				// Removed: scaling turned off.
				Config:           cfg(""),
				ConfigPlanChecks: instPlan(plancheck.ResourceActionUpdate),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue("mtncloud_instance.app", attr("scale"), knownvalue.Null()),
				},
			},
			{
				PreConfig: func() {
					if th := threshold(); th["cpuEnabled"] != false || th["memoryEnabled"] != false || th["autoUp"] != false {
						t.Errorf("threshold after removing scale %v", th)
					}
				},
				Config:      strings.Replace(cfg(""), "expire_days = 30", "expire_days = 60", 1),
				PlanOnly:    true,
				ExpectError: regexp.MustCompile(`(?s)Set at create only.*set\s+expire_days\s+back\s+to\s+30`),
			},
			{
				// A replacing change takes the new days with it: no refusal.
				Config: strings.Replace(strings.Replace(cfg(""), "expire_days = 30", "expire_days = 60", 1),
					`networks = ["9301"]`, `networks = ["9303"]`, 1),
				PlanOnly: true, ExpectNonEmptyPlan: true, // no "Set at create only" error
			},
			{
				// Scaling changed in the console: the plan puts it back.
				PreConfig: func() {
					s.mu.Lock()
					defer s.mu.Unlock()
					for id := range s.insts {
						s.thresholds[id] = map[string]any{"autoUp": true, "minCount": 1, "maxCount": 5, "scaleIncrement": 1, "cpuEnabled": true, "minCpu": 10.0, "maxCpu": 90.0}
					}
				},
				Config:           cfg(""),
				ConfigPlanChecks: instPlan(plancheck.ResourceActionUpdate),
			},
			{Config: cfg(`  scale = {
    min_count = 3
    max_count = 2
  }`), PlanOnly: true, ExpectError: regexp.MustCompile(`(?s)(max_count\s+must\s+be\s+at\s+least\s+min_count.*Watch\s+at\s+least\s+one|Watch\s+at\s+least\s+one.*max_count\s+must\s+be\s+at\s+least\s+min_count)`)},
			{Config: f.providerConfig()},
		},
	})
}

func TestInstance_LifecycleEdges(t *testing.T) {
	f, s := newInstFakes(t)
	cfg := func(extra string) string {
		return instConfig(f, `  name = "app-1"
  plan = "G1S1"
  networks = ["9301"]
`+extra)
	}
	scale := `  scale = {
    min_count = 1
    max_count = 2
    cpu       = { min = 20, max = 80 }
  }`
	setErr := func(on bool) func() { return func() { s.mu.Lock(); defer s.mu.Unlock(); s.thresholdErr = on } }
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories(),
		Steps: []resource.TestStep{
			{
				// The scaling PUT fails: a warning, so the VM is kept (not tainted)...
				PreConfig:          setErr(true),
				Config:             cfg(scale),
				ExpectNonEmptyPlan: true, // scaling still to set
			},
			{
				// ...and the next plan sets the scaling in place.
				PreConfig:        setErr(false),
				Config:           cfg(scale),
				ConfigPlanChecks: instPlan(plancheck.ResourceActionUpdate),
			},
			{
				// No days in state (as after an import): setting them only warns, and changes nothing.
				Config:           cfg(scale + "\n  expire_days = 60"),
				ConfigPlanChecks: instPlan(plancheck.ResourceActionUpdate),
			},
			{
				Config:      cfg(scale + "\n  expire_days = 90"),
				PlanOnly:    true,
				ExpectError: regexp.MustCompile(`(?s)Set at create only.*set\s+expire_days\s+back\s+to\s+60`),
			},
			{Config: f.providerConfig()},
		},
	})
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.creates != 1 {
		t.Errorf("%d creates, want 1: a failed scaling PUT must not replace the VM", s.creates)
	}
}

// floating fakes the floating IP calls on an instance's container (container ID = instance ID*10+7).
func (s *instStore) floating(w http.ResponseWriter, r *http.Request, reply func(int, any)) {
	if strings.HasPrefix(r.URL.Path, "/api/options/") {
		if r.URL.Query().Get("containerId") == "" {
			reply(http.StatusInternalServerError, map[string]any{"success": false, "msg": "Looks like the server threw a gasket"})
			return
		}
		reply(http.StatusOK, map[string]any{"success": true, "data": []any{
			map[string]any{"name": "Pools", "value": ""},
			map[string]any{"name": "public-network-01", "value": "pool-9044"},
			map[string]any{"name": "public-network-02", "value": "pool-9043"},
			map[string]any{"name": "Available Ips", "value": ""},
			map[string]any{"name": "203.0.113.4", "value": "ip-96420"},
		}})
		return
	}
	parts := strings.Split(strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/containers/"), "/"), "/")
	cid, _ := strconv.ParseInt(parts[0], 10, 64)
	in := s.insts[(cid-7)/10]
	if in == nil || len(parts) != 2 {
		reply(http.StatusNotFound, map[string]any{"success": false, "msg": "Container not found"})
		return
	}
	private := in["connectionInfo"].([]any)[0].(map[string]any)["ip"]
	for _, e := range in["evars"].([]any) {
		if ev := e.(map[string]any); strings.HasSuffix(ev["name"].(string), "_IP") {
			private = ev["value"]
		}
	}
	switch parts[1] {
	case "attach-floating-ip":
		s.attachCalls++
		if s.fipErr {
			reply(http.StatusBadRequest, map[string]any{"success": false, "msg": "No router with an external gateway for this network"})
			return
		}
		if s.fipSilent {
			break
		}
		in["connectionInfo"] = []any{map[string]any{"ip": fmt.Sprintf("203.0.113.%d", cid%200), "port": 22, "name": "SSH"}}
		s.attached = append(s.attached, cid)
		if s.fip504 {
			reply(http.StatusGatewayTimeout, map[string]any{"success": false, "msg": "Gateway Timeout"})
			return
		}
	case "detach-floating-ip":
		if !s.detachStuck {
			in["connectionInfo"] = []any{map[string]any{"ip": private, "port": 22, "name": "SSH"}}
		}
	}
	reply(http.StatusOK, map[string]any{"success": true})
}

func TestInstance_FloatingIP(t *testing.T) {
	f, s := newInstFakes(t)
	lock := func(fn func()) func() { return func() { s.mu.Lock(); defer s.mu.Unlock(); fn() } }
	cfg := func(extra string) string {
		return instConfig(f, `  name = "app-1"
  plan = "G1S1"
  networks = ["9301"]
`+extra)
	}
	ip := func(re string) []statecheck.StateCheck {
		var check knownvalue.Check = knownvalue.Null()
		if re != "" {
			check = knownvalue.StringRegexp(regexp.MustCompile(re))
		}
		return []statecheck.StateCheck{statecheck.ExpectKnownValue("mtncloud_instance.app", tfjsonpath.New("public_ip"), check)}
	}
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories(),
		Steps: []resource.TestStep{
			{
				// Attached after the VM runs, never in the create body.
				Config:            cfg(`  floating_ip_pool = "public-network-02"`),
				ConfigStateChecks: ip(`^203\.0\.113\.`),
			},
			{
				PreConfig: lock(func() {
					if _, ok := s.lastCreate["config"].(map[string]any)["osExternalNetworkId"]; ok {
						t.Error("a floating IP was asked for in the create body")
					}
				}),
				Config:           cfg(`  floating_ip_pool = "PUBLIC-network-02"`),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()}},
			},
			{
				// Another pool: in place.
				Config:            cfg(`  floating_ip_pool = "public-network-01"`),
				ConfigPlanChecks:  instPlan(plancheck.ResourceActionUpdate),
				ConfigStateChecks: ip(`^203\.0\.113\.`),
			},
			{
				// Released in the console: every attach was confirmed, so this is drift, fixed in place.
				PreConfig: lock(func() {
					for _, in := range s.insts {
						in["connectionInfo"] = []any{map[string]any{"ip": "10.20.1.99"}}
						in["evars"] = append(in["evars"].([]any), map[string]any{"name": in["environmentPrefix"].(string) + "_IP", "value": "10.20.1.99"})
					}
				}),
				Config:            cfg(`  floating_ip_pool = "public-network-01"`),
				ConfigPlanChecks:  instPlan(plancheck.ResourceActionUpdate),
				ConfigStateChecks: ip(`^203\.0\.113\.`),
			},
			{
				// Removed: detached.
				Config:            cfg(""),
				ConfigPlanChecks:  instPlan(plancheck.ResourceActionUpdate),
				ConfigStateChecks: ip(""),
			},
			{Config: cfg(`  floating_ip_pool = "nope"`), PlanOnly: true,
				ExpectError: regexp.MustCompile(`(?s)Unknown floating IP pool.*public-network-01,\s+public-network-02`)},
			{Config: f.providerConfig()},
			{
				// No router: the apply that asked for the address fails, and the VM is tainted.
				PreConfig:   lock(func() { s.fipErr = true }),
				Config:      cfg(`  floating_ip_pool = "public-network-02"`),
				ExpectError: regexp.MustCompile(`(?s)Floating IP not attached.*No router with an\s+external gateway`),
			},
			{
				// The router is fixed: the tainted VM is replaced, and the new one gets its address.
				PreConfig:         lock(func() { s.fipErr = false }),
				Config:            cfg(`  floating_ip_pool = "public-network-02"`),
				ConfigPlanChecks:  instPlan(plancheck.ResourceActionDestroyBeforeCreate),
				ConfigStateChecks: ip(`^203\.0\.113\.`),
			},
			{Config: f.providerConfig()},
		},
	})
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.creates != 3 {
		t.Errorf("%d creates, want 3: the tainted VM is replaced once", s.creates)
	}
}

func TestInstance_FloatingIPFailures(t *testing.T) {
	f, s := newInstFakes(t)
	lock := func(fn func()) func() { return func() { s.mu.Lock(); defer s.mu.Unlock(); fn() } }
	cfg := func(extra string) string {
		return instConfig(f, `  name = "app-1"
  plan = "G1S1"
  networks = ["9301"]
`+extra)
	}
	pool := func(want knownvalue.Check) []statecheck.StateCheck {
		return []statecheck.StateCheck{statecheck.ExpectKnownValue("mtncloud_instance.app", tfjsonpath.New("floating_ip_pool"), want)}
	}
	attaches, calls := 0, 0
	countAttaches := func(want int) func() {
		return lock(func() {
			if len(s.attached) != want {
				t.Errorf("%d attaches, want %d", len(s.attached), want)
			}
		})
	}
	empty := resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()}}
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories(),
		Steps: []resource.TestStep{
			{Config: cfg(`  floating_ip_pool = "public-network-02"`)},
			{
				// The old address is released, the new one fails: state records none.
				PreConfig:   lock(func() { s.fipErr = true }),
				Config:      cfg(`  floating_ip_pool = "public-network-01"`),
				ExpectError: regexp.MustCompile(`(?s)Unable to change the floating IP.*MTN Router`),
			},
			{
				PreConfig:          lock(func() { s.fipErr = false }),
				RefreshState:       true,
				ExpectNonEmptyPlan: true, // the attach is retried
				Check:              resource.TestCheckNoResourceAttr("mtncloud_instance.app", "floating_ip_pool"),
			},
			{
				Config:           cfg(`  floating_ip_pool = "public-network-01"`),
				ConfigPlanChecks: instPlan(plancheck.ResourceActionUpdate),
			},
			{Config: cfg("")},
			{
				// Attached in the console, then configured: adopted, never a second address.
				PreConfig: lock(func() {
					for _, in := range s.insts {
						in["connectionInfo"] = []any{map[string]any{"ip": "203.0.113.250", "port": 22, "name": "SSH"}}
					}
					attaches = len(s.attached)
				}),
				Config:            cfg(`  floating_ip_pool = "public-network-02"`),
				ConfigPlanChecks:  instPlan(plancheck.ResourceActionUpdate),
				ConfigStateChecks: pool(knownvalue.StringExact("public-network-02")),
			},
			{
				PreConfig:        func() { countAttaches(attaches)() },
				Config:           cfg(`  floating_ip_pool = "public-network-02"`),
				ConfigPlanChecks: empty,
			},
			{Config: f.providerConfig()},
			{
				// A gateway error after the attach was applied: the wait sees the address, so it worked.
				PreConfig: lock(func() { s.fip504 = true }),
				Config:    cfg(`  floating_ip_pool = "public-network-02"`),
			},
			{
				PreConfig:        lock(func() { s.fip504 = false }),
				Config:           cfg(`  floating_ip_pool = "public-network-02"`),
				ConfigPlanChecks: empty,
			},
			{
				// Accepted but never shows: the apply fails when the update timeout ends.
				PreConfig: lock(func() { s.fipSilent = true }),
				Config: cfg(`  floating_ip_pool = "public-network-01"
  timeouts {
    update = "3s"
  }`),
				ExpectError: regexp.MustCompile(`(?s)Unable to change the floating IP.*waiting for the floating IP.*raise the\s+timeout`),
			},
			{
				// The address may still arrive: a warning, and the attach is never sent again.
				PreConfig: lock(func() { calls = s.attachCalls }),
				Config: cfg(`  floating_ip_pool = "public-network-01"
  timeouts {
    update = "3s"
  }`),
				ConfigPlanChecks: empty,
			},
			{
				PreConfig: lock(func() {
					if s.attachCalls != calls {
						t.Errorf("attach sent %d more times after an unconfirmed attach, want 0", s.attachCalls-calls)
					}
					s.fipSilent = false
				}),
				Config: f.providerConfig(),
			},
			{Config: cfg(`  floating_ip_pool = "public-network-02"`)},
			{
				// A release that never shows: the old pool stays recorded, as the address is still there.
				PreConfig: lock(func() { s.detachStuck = true }),
				Config: cfg(`  floating_ip_pool = "public-network-01"
  timeouts {
    update = "3s"
  }`),
				ExpectError: regexp.MustCompile(`(?s)waiting for the floating IP to be released`),
			},
			{
				PreConfig:          lock(func() { s.detachStuck = false }),
				RefreshState:       true,
				ExpectNonEmptyPlan: true,
				Check:              resource.TestCheckResourceAttr("mtncloud_instance.app", "floating_ip_pool", "public-network-02"),
			},
			{
				// A gateway error on an update's attach that was applied: the wait sees it, so it worked.
				PreConfig:         lock(func() { s.fip504 = true }),
				Config:            cfg(`  floating_ip_pool = "public-network-01"`),
				ConfigStateChecks: pool(knownvalue.StringExact("public-network-01")),
			},
			{PreConfig: lock(func() { s.fip504 = false }), Config: f.providerConfig()},
		},
	})
}
