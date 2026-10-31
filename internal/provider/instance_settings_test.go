package provider

import (
	"regexp"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/knownvalue"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/statecheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"
)

// seedOrderVM puts in the fake a VM something else made (a catalog order's), as the API lists it.
func seedOrderVM(s *instStore, id int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.insts[id] = map[string]any{
		"id": id, "name": "ab-42-MTN-CLOUD-MYSQL-1", "hostName": "ab-42-mtn-cloud-mysql-1", "status": "running",
		"environmentPrefix": "MTN_UBUNTU", "description": "made by an order", "labels": []any{"order"},
		"tags":           []any{map[string]any{"name": "team", "value": "db"}},
		"group":          map[string]any{"id": 1, "name": "MTNNG_CLOUD_AZ_1"},
		"cloud":          map[string]any{"id": 4, "name": "MTNNG_CLOUD_AZ_1"},
		"instanceType":   map[string]any{"code": "MTN-U24.04LTS", "name": fakeImages["MTN-U24.04LTS"]},
		"plan":           map[string]any{"id": 9401.0, "name": "G1S1"},
		"config":         map[string]any{"resourcePoolId": "pool-9201", "noAgent": true, "createUser": true, "layoutSize": 1},
		"containers":     []any{id*10 + 7},
		"volumes":        []any{s.volume(id, 0, map[string]any{"name": "root", "size": 50, "rootVolume": true, "datastoreId": "auto"})},
		"interfaces":     []any{map[string]any{"id": "1", "network": map[string]any{"id": 9301}, "primaryInterface": true}},
		"connectionInfo": []any{map[string]any{"ip": "10.0.0.50", "port": 22, "name": "SSH"}},
		"evars":          []any{map[string]any{"name": "MTN_UBUNTU_IP", "value": "10.0.0.50"}},
	}
	s.sgs[id] = []int64{9301}
}

func settingsConfig(f *fakeAPI, body string) string {
	return f.providerConfig() + "resource \"mtncloud_instance_settings\" \"db\" {\n  instance_id = \"9750\"\n" + body + "\n}\n"
}

func settingsAttr(a string, v knownvalue.Check) statecheck.StateCheck {
	return statecheck.ExpectKnownValue("mtncloud_instance_settings.db", tfjsonpath.New(a), v)
}

// Adopting sends nothing; set settings are applied in place and held against the console; destroy
// leaves the VM (B-028).
func TestInstanceSettings_Lifecycle(t *testing.T) {
	f, s := newInstFakes(t)
	seedOrderVM(s, 9750)
	empty := resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()}}
	update := resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
		plancheck.ExpectResourceAction("mtncloud_instance_settings.db", plancheck.ResourceActionUpdate)}}
	managed := `  plan            = "g2s4"
  security_groups = ["web"]
  labels          = ["order", "reporting"]
  power_state     = "stopped"`
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories(),
		CheckDestroy: func(*terraform.State) error {
			if s.count() != 1 {
				t.Error("destroy deleted the VM: it belongs to whatever made it")
			}
			return nil
		},
		Steps: []resource.TestStep{
			{Config: settingsConfig(f, ""), ConfigStateChecks: []statecheck.StateCheck{
				settingsAttr("plan", knownvalue.StringExact("G1S1")),
				settingsAttr("security_groups", knownvalue.Null()), // only the project's default
				settingsAttr("tags", knownvalue.MapExact(map[string]knownvalue.Check{"team": knownvalue.StringExact("db")})),
				settingsAttr("power_state", knownvalue.StringExact("running")),
			}, Check: func(*terraform.State) error {
				s.mu.Lock()
				defer s.mu.Unlock()
				if len(s.resizes) != 0 || len(s.powers) != 0 {
					t.Errorf("adopting sent %d resizes and powers %v, want nothing", len(s.resizes), s.powers)
				}
				return nil
			}},
			{Config: settingsConfig(f, ""), ConfigPlanChecks: empty},
			{Config: settingsConfig(f, managed), ConfigPlanChecks: update, ConfigStateChecks: []statecheck.StateCheck{
				settingsAttr("plan", knownvalue.StringExact("g2s4")),
				settingsAttr("security_groups", knownvalue.SetExact([]knownvalue.Check{knownvalue.StringExact("web")})),
				settingsAttr("labels", knownvalue.SetSizeExact(2)),
				settingsAttr("tags", knownvalue.MapSizeExact(1)), // not set: kept
				settingsAttr("power_state", knownvalue.StringExact("stopped")),
			}, Check: func(*terraform.State) error {
				s.mu.Lock()
				defer s.mu.Unlock()
				if len(s.resizes) != 1 {
					t.Fatalf("%d resizes, want 1", len(s.resizes))
				}
				vols := s.resizes[0]["volumes"].([]any)
				if len(vols) != 1 || vols[0].(map[string]any)["id"] == -1.0 {
					t.Errorf("resize sent volumes %v: want the root disk with its ID, nothing added", vols)
				}
				return nil
			}},
			{Config: settingsConfig(f, managed), ConfigPlanChecks: empty},
			// Started in the console: the next apply stops it again.
			{PreConfig: func() { s.mu.Lock(); s.insts[9750]["status"] = "running"; s.mu.Unlock() },
				Config: settingsConfig(f, managed), ConfigPlanChecks: update,
				ConfigStateChecks: []statecheck.StateCheck{settingsAttr("power_state", knownvalue.StringExact("stopped"))}},
			// Import reads the server's spelling of the plan ("G2S4"); the configured "g2s4" is kept from then on.
			{Config: settingsConfig(f, managed), ResourceName: "mtncloud_instance_settings.db", ImportState: true, ImportStateId: "9750",
				ImportStateVerify: true, ImportStateVerifyIgnore: []string{"timeouts", "plan"}},
			// Removing a setting stops managing it: nothing is sent, the VM keeps it.
			{Config: settingsConfig(f, `  plan = "g2s4"`), Check: func(*terraform.State) error {
				s.mu.Lock()
				defer s.mu.Unlock()
				if got := s.insts[9750]["status"]; got != "stopped" {
					t.Errorf("status %v after power_state was removed, want it left stopped", got)
				}
				return nil
			}},
		},
	})
}

// The VM deleted with whatever owned it: the settings leave state, and a plan shows them again.
func TestInstanceSettings_Disappears(t *testing.T) {
	f, s := newInstFakes(t)
	seedOrderVM(s, 9750)
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories(),
		Steps: []resource.TestStep{
			{Config: settingsConfig(f, `  labels = ["order"]`)},
			{PreConfig: func() { s.mu.Lock(); delete(s.insts, 9750); s.mu.Unlock() },
				Config: settingsConfig(f, `  labels = ["order"]`), PlanOnly: true, ExpectNonEmptyPlan: true},
		},
	})
}

// A floating IP and scaling on an ordered VM: attached and set in place, an address released in the
// console is attached again, and removing the settings leaves both as they are (B-028).
func TestInstanceSettings_FloatingIPAndScale(t *testing.T) {
	f, s := newInstFakes(t)
	seedOrderVM(s, 9750)
	empty := resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()}}
	managed := `  floating_ip_pool = "public-network-02"
  scale = {
    min_count = 1
    max_count = 3
    cpu       = { min = 30, max = 75 }
  }`
	attachCalls := func() int { s.mu.Lock(); defer s.mu.Unlock(); return s.attachCalls }
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories(),
		Steps: []resource.TestStep{
			{Config: settingsConfig(f, ""), ConfigStateChecks: []statecheck.StateCheck{
				settingsAttr("scale", knownvalue.Null()), settingsAttr("public_ip", knownvalue.Null())}},
			{Config: settingsConfig(f, managed), ConfigStateChecks: []statecheck.StateCheck{
				settingsAttr("public_ip", knownvalue.StringExact("203.0.113.107")),
				settingsAttr("scale", knownvalue.NotNull()),
			}, Check: func(*terraform.State) error {
				s.mu.Lock()
				defer s.mu.Unlock()
				if th := s.thresholds[9750]; th == nil || th["maxCount"] != 3.0 {
					t.Errorf("threshold %v, want max 3", th)
				}
				return nil
			}},
			{Config: settingsConfig(f, managed), ConfigPlanChecks: empty},
			// Scaling changed in the console: the plan puts it back.
			{PreConfig: func() { s.mu.Lock(); s.thresholds[9750]["maxCount"] = 9.0; s.mu.Unlock() },
				Config: settingsConfig(f, managed), ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction("mtncloud_instance_settings.db", plancheck.ResourceActionUpdate)}}},
			// Released in the console: the next apply attaches one again.
			{PreConfig: func() {
				s.mu.Lock()
				s.insts[9750]["connectionInfo"] = []any{map[string]any{"ip": "10.0.0.50", "port": 22, "name": "SSH"}}
				s.mu.Unlock()
			}, Config: settingsConfig(f, managed), Check: func(*terraform.State) error {
				if n := attachCalls(); n != 2 {
					t.Errorf("%d attaches, want 2 (the first, and after the release)", n)
				}
				return nil
			}},
			{Config: settingsConfig(f, managed), ConfigPlanChecks: empty},
			// Removed from the configuration: nothing is released or turned off.
			{Config: settingsConfig(f, ""), ConfigStateChecks: []statecheck.StateCheck{
				settingsAttr("public_ip", knownvalue.StringExact("203.0.113.107")),
				settingsAttr("scale", knownvalue.Null()), // not managed, not shown
			}, Check: func(*terraform.State) error {
				s.mu.Lock()
				defer s.mu.Unlock()
				if th := s.thresholds[9750]; th == nil || th["maxCount"] != 3.0 {
					t.Errorf("threshold %v after scale was removed, want it left as it was", th)
				}
				return nil
			}},
			{Config: settingsConfig(f, ""), ConfigPlanChecks: empty},
			{Config: settingsConfig(f, `  scale = {
    min_count = 3
    max_count = 1
    cpu       = { min = 30, max = 75 }
  }`), ExpectError: regexp.MustCompile(`max_count must be at least min_count`)},
			{Config: settingsConfig(f, "")},
		},
	})
}

// A data disk added to an ordered VM attaches in place; a disk changed or removed is refused at plan;
// with no data_volume blocks, disks aren't managed (B-028).
func TestInstanceSettings_DataDisks(t *testing.T) {
	f, s := newInstFakes(t)
	seedOrderVM(s, 9750)
	empty := resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()}}
	disk := func(sizes ...string) string {
		out := ""
		for _, sz := range sizes {
			out += "  data_volume {\n    size = " + sz + "\n  }\n"
		}
		return out
	}
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories(),
		Steps: []resource.TestStep{
			{Config: settingsConfig(f, ""), ConfigStateChecks: []statecheck.StateCheck{settingsAttr("data_volume", knownvalue.ListSizeExact(0))}},
			{Config: settingsConfig(f, disk("30")), ConfigStateChecks: []statecheck.StateCheck{
				settingsAttr("data_volume", knownvalue.ListSizeExact(1)),
			}, Check: func(*terraform.State) error {
				s.mu.Lock()
				defer s.mu.Unlock()
				if len(s.resizes) != 1 {
					t.Fatalf("%d resizes, want 1", len(s.resizes))
				}
				vols := s.resizes[0]["volumes"].([]any)
				if len(vols) != 2 || vols[1].(map[string]any)["size"] != 30.0 || vols[0].(map[string]any)["id"] == -1.0 {
					t.Errorf("resize sent %v: want the root disk with its ID, then a new 30 GB disk", vols)
				}
				return nil
			}},
			{Config: settingsConfig(f, disk("30")), ConfigPlanChecks: empty},
			{Config: settingsConfig(f, disk("40")), PlanOnly: true, ExpectError: regexp.MustCompile(`Disk can't change in place`)},
			{Config: settingsConfig(f, ""), ConfigStateChecks: []statecheck.StateCheck{settingsAttr("data_volume", knownvalue.ListSizeExact(0))},
				Check: func(*terraform.State) error {
					s.mu.Lock()
					defer s.mu.Unlock()
					if n := len(s.insts[9750]["volumes"].([]any)); n != 2 {
						t.Errorf("%d volumes after data_volume was removed, want the disk kept (2)", n)
					}
					return nil
				}},
			{Config: settingsConfig(f, ""), ConfigPlanChecks: empty},
		},
	})
}

// restart_trigger set when adopting (and after an import) doesn't restart someone else's VM; a
// change to it does.
func TestInstanceSettings_RestartTrigger(t *testing.T) {
	f, s := newInstFakes(t)
	seedOrderVM(s, 9750)
	powers := func() int { s.mu.Lock(); defer s.mu.Unlock(); return len(s.powers) }
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories(),
		Steps: []resource.TestStep{
			{Config: settingsConfig(f, `  restart_trigger = "v1"`), Check: func(*terraform.State) error {
				if n := powers(); n != 0 {
					t.Errorf("adopting with restart_trigger sent %d power actions, want 0", n)
				}
				return nil
			}},
			{Config: settingsConfig(f, `  restart_trigger = "v2"`), Check: func(*terraform.State) error {
				s.mu.Lock()
				defer s.mu.Unlock()
				if len(s.powers) != 1 || s.powers[0] != "restart" {
					t.Errorf("power actions %v after restart_trigger changed, want [restart]", s.powers)
				}
				return nil
			}},
		},
	})
}

// An attach that is sent but never shows is recorded as asked: the next apply waits for it instead
// of attaching a second address.
func TestInstanceSettings_FloatingIPUnconfirmed(t *testing.T) {
	f, s := newInstFakes(t)
	seedOrderVM(s, 9750)
	lock := func(fn func()) func() { return func() { s.mu.Lock(); defer s.mu.Unlock(); fn() } }
	cfg := settingsConfig(f, `  floating_ip_pool = "public-network-01"
  timeouts {
    update = "3s"
  }`)
	var calls int
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories(),
		Steps: []resource.TestStep{
			{Config: settingsConfig(f, "")},
			{PreConfig: lock(func() { s.fipSilent = true }), Config: cfg,
				ExpectError: regexp.MustCompile(`(?s)Floating IP not confirmed.*waiting for the floating IP`)},
			{PreConfig: lock(func() { calls = s.attachCalls }), Config: cfg,
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()}}},
			// Another change in the same apply doesn't send the attach again either.
			{Config: strings.Replace(cfg, "  timeouts {", "  labels = [\"order\", \"web\"]\n  timeouts {", 1)},
			{PreConfig: lock(func() {
				if s.attachCalls != calls {
					t.Errorf("attach sent %d more times after an unconfirmed attach, want 0", s.attachCalls-calls)
				}
				s.fipSilent = false
			}), Config: settingsConfig(f, "")},
		},
	})
}

// A resize that leaves the instance failed fails the apply with the platform's message, and a
// change applied with it (restart_trigger) is not recorded as done.
func TestInstanceSettings_ResizeFailed(t *testing.T) {
	f, s := newInstFakes(t)
	seedOrderVM(s, 9750)
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories(),
		Steps: []resource.TestStep{
			{Config: settingsConfig(f, `  restart_trigger = "v1"`)},
			{PreConfig: func() { s.mu.Lock(); s.resizeFails = true; s.mu.Unlock() },
				Config: settingsConfig(f, `  plan            = "g2s4"
  restart_trigger = "v2"`),
				ExpectError: regexp.MustCompile(`(?s)Instance change failed.*"failed": No valid host was found`)},
			{PreConfig: func() {
				s.mu.Lock()
				s.resizeFails = false
				s.insts[9750]["status"] = "running"
				s.mu.Unlock()
			}, Config: settingsConfig(f, `  restart_trigger = "v2"`), PlanOnly: true, ExpectNonEmptyPlan: true},
		},
	})
}

// Several servers: a floating IP is attached to each, public_ips lists them, and a server that
// lost its address in the console gets one again, without a second for the other (B-013).
func TestInstanceSettings_SeveralServers(t *testing.T) {
	f, s := newInstFakes(t)
	seedOrderVM(s, 9750)
	s.mu.Lock()
	s.insts[9750]["containers"] = []any{97507, 97508}
	s.insts[9750]["config"].(map[string]any)["layoutSize"] = 2
	s.mu.Unlock()
	cfg := settingsConfig(f, `  floating_ip_pool = "public-network-02"`)
	empty := resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()}}
	attached := func() []int64 { s.mu.Lock(); defer s.mu.Unlock(); return append([]int64{}, s.attached...) }
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories(),
		Steps: []resource.TestStep{
			{Config: cfg, ConfigStateChecks: []statecheck.StateCheck{
				settingsAttr("public_ips", knownvalue.ListExact([]knownvalue.Check{
					knownvalue.StringExact("203.0.113.107"), knownvalue.StringExact("203.0.113.108")})),
			}, Check: func(*terraform.State) error {
				if a := attached(); len(a) != 2 {
					t.Errorf("attached to %v, want both servers", a)
				}
				return nil
			}},
			{Config: cfg, ConfigPlanChecks: empty},
			// The second server's address released in the console: only it gets one again.
			{PreConfig: func() { s.mu.Lock(); delete(s.serverIP, 97508); s.mu.Unlock() }, Config: cfg,
				ConfigStateChecks: []statecheck.StateCheck{settingsAttr("public_ips", knownvalue.ListSizeExact(2))},
				Check: func(*terraform.State) error {
					if a := attached(); len(a) != 3 || a[2] != 97508 {
						t.Errorf("attached to %v, want the second server once more", a)
					}
					return nil
				}},
			{Config: cfg, ConfigPlanChecks: empty},
		},
	})
}

// Two servers, one attach seen and the other never: the address still on its way isn't asked for
// again, even after an apply that changes something else (one server's address isn't all of them).
func TestInstanceSettings_SeveralServersUnconfirmed(t *testing.T) {
	f, s := newInstFakes(t)
	seedOrderVM(s, 9750)
	s.mu.Lock()
	s.insts[9750]["containers"] = []any{97507, 97508}
	s.mu.Unlock()
	lock := func(fn func()) func() { return func() { s.mu.Lock(); defer s.mu.Unlock(); fn() } }
	body := `  floating_ip_pool = "public-network-02"
  timeouts {
    update = "3s"
    create = "3s"
  }`
	var calls int
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories(),
		Steps: []resource.TestStep{
			{Config: settingsConfig(f, "")},
			{PreConfig: lock(func() { s.silentServer = 97508 }), Config: settingsConfig(f, body),
				ExpectError: regexp.MustCompile(`Floating IP not confirmed`)},
			{PreConfig: lock(func() { calls = s.attachCalls }),
				Config: settingsConfig(f, `  labels = ["order", "web"]
`+body)},
			{Config: settingsConfig(f, `  labels = ["order", "web"]
`+body), PlanOnly: true},
			{PreConfig: lock(func() {
				if s.attachCalls != calls {
					t.Errorf("attach sent %d more times while one was on its way, want 0", s.attachCalls-calls)
				}
				s.silentServer = 0
			}), Config: settingsConfig(f, "")},
		},
	})
}
