package provider

import (
	"encoding/json"
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

// PfSense (item 53, renamed from "MTN VPN Service"; found by its code) fixes its disks in hidden
// form fields, names its plans its own way, and needs two networks (its help text).
func TestPfSenseOrder(t *testing.T) {
	f, s, _ := orderFakes(t)
	f.net.(*netStore).networks[9351] = map[string]any{"id": 9351, "name": "dmz-net", "status": "ACTIVE",
		"zonePool": map[string]any{"id": 9201, "name": "web-prod-Acme"}}
	cfg := func(plan, nets string) string {
		return f.providerConfig() + `
resource "mtncloud_pfsense_vpn" "vpn" {
  name             = "vpn-1"
  group            = "MTNNG_CLOUD_AZ_1"
  project          = "web-prod-Acme"
  plan             = "` + plan + `"
  networks         = [` + nets + `]
  floating_ip_pool = "public-network-01"
}
`
	}
	two := `"app-net", "dmz-net"`
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories(),
		Steps: []resource.TestStep{
			{Config: cfg("G2S4", `"app-net"`), PlanOnly: true,
				ExpectError: regexp.MustCompile(wrapped(`(?s)Too few networks.*at least 2 networks; 1 is set.*The form says: Note: Service requires at least 2`))},
			// A network by ID (as one made in the same apply is given) works like one by name.
			{Config: cfg("G2S4", `"app-net", "9351"`), PlanOnly: true, ExpectNonEmptyPlan: true},
			{Config: cfg("G2S4", `"app-net", "99999"`), PlanOnly: true,
				ExpectError: regexp.MustCompile(wrapped(`(?s)no network named "99999", or with that ID.*give its ID`))},
			{Config: cfg("XL", two), PlanOnly: true,
				ExpectError: regexp.MustCompile(wrapped(`(?s)no plan "XL": choose one of Small \(G2S4\), Medium \(G4S4\), Large\s+\(G4S8\)`))},
			{
				// "G2S4" picks "Small (G2S4)": the size code inside the form's own name.
				Config: cfg("G2S4", two),
				Check: func(*terraform.State) error {
					s.mu.Lock()
					defer s.mu.Unlock()
					item := s.sent[9801]
					cfg, _ := item["config"].(map[string]any)
					if name, _ := item["type"].(map[string]any)["name"].(string); name != "MTN PfSense VPN" {
						t.Errorf("ordered %q, want the item's name now", name)
					}
					if cfg["plan"] != float64(13107) || cfg["name"] != "vpn-1" || cfg["volume"] != float64(53687091200) {
						t.Errorf("plan %v, name %v, volume %v", cfg["plan"], cfg["name"], cfg["volume"])
					}
					vols, _ := json.Marshal(cfg["volumes"])
					if string(vols) != `[{"name":"root","rootVolume":true,"size":50}]` {
						t.Errorf("volumes %s, want the form's hidden 50 GB root disk", vols)
					}
					return nil
				},
			},
			{Config: cfg("G2S4", two), ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()}}},
			{Config: forget(f, "mtncloud_pfsense_vpn.vpn")},
		},
	})
}

// MongoDB: a field required only while another is on (adminPassword while enableAuth), a field
// used only while another is on (replicaSetName), the form's pre-filled values, and a required
// field this provider has no argument for (extra_inputs).
func TestOrder_FormRules(t *testing.T) {
	f, s, _ := orderFakes(t)
	cfg := func(extra string) string {
		return f.providerConfig() + `
resource "mtncloud_mongodb" "db" {
  name             = "mongo-1"
  group            = "MTNNG_CLOUD_AZ_1"
  project          = "web-prod-Acme"
  plan             = "G2S4"
  root_volume_size = 40
  networks         = ["app-net"]
` + extra + `}
`
	}
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories(),
		Steps: []resource.TestStep{
			{Config: cfg(`  extra_inputs = { newField = "x" }
`), PlanOnly: true,
				ExpectError: regexp.MustCompile(wrapped(`(?s)Missing required input.*"Admin Password" \(adminPassword\) when enableAuth is on.*Required if enableAuth`))},
			{Config: cfg(`  admin_password = "s3cret"
`), PlanOnly: true,
				ExpectError: regexp.MustCompile(wrapped(`(?s)requires "New Field" \(newField\).*set it in extra_inputs, as "newField"`))},
			{Config: cfg(`  enable_auth  = false
  extra_inputs = { newField = "x", nope = "y" }
`), PlanOnly: true, ExpectError: regexp.MustCompile(wrapped(`(?s)has no field "nope"`))},
			{Config: cfg(`  enable_auth  = false
  extra_inputs = { newField = "x", adminUsername = "y" }
`), PlanOnly: true, ExpectError: regexp.MustCompile(wrapped(`(?s)Set "adminUsername" with the admin_username argument`))},
			// The disks have an argument too, though its form key (volumes) isn't its name.
			{Config: cfg(`  enable_auth  = false
  extra_inputs = { newField = "x", volumes = "[]" }
`), PlanOnly: true, ExpectError: regexp.MustCompile(wrapped(`(?s)Set "volumes" with the root_volume_size argument`))},
			// Required only while another field is on: oplogSizeMB with a replica set.
			{Config: cfg(`  enable_auth  = false
  replica_set  = true
  extra_inputs = { newField = "x" }
`), PlanOnly: true, ExpectError: regexp.MustCompile(wrapped(`(?s)requires "Oplog Size" \(oplogSizeMB\) when initReplicaSet is on`))},
			{
				// Auth off: no password needed, and a replica set name given without a replica set
				// isn't sent (a warning says so).
				Config: cfg(`  enable_auth      = false
  replica_set_name = "rs9"
  allowed_cidrs    = ["10.0.0.0/8", "172.16.0.0/12"]
  extra_inputs     = { newField = "x" }
`),
				Check: func(*terraform.State) error {
					s.mu.Lock()
					defer s.mu.Unlock()
					c, _ := s.sent[9801]["config"].(map[string]any)
					if c["newField"] != "x" || c["adminUsername"] != "Admin" || c["mongoUserRole"] != "readWrite" || c["enableAuth"] != "off" {
						t.Errorf("newField %v, adminUsername %v, mongoUserRole %v, enableAuth %v", c["newField"], c["adminUsername"], c["mongoUserRole"], c["enableAuth"])
					}
					if _, sent := c["replicaSetName"]; sent {
						t.Error("replicaSetName sent without initReplicaSet")
					}
					if _, sent := c["adminPassword"]; sent {
						t.Error("adminPassword sent")
					}
					// Lists that can't be blank send their first choice, as the console shows them,
					// also one shown only for that choice; one shown for another choice isn't sent.
					// Several CIDRs go comma-separated, as the form's pattern asks.
					if c["hostCIDRs"] != "10.0.0.0/8, 172.16.0.0/12" {
						t.Errorf("hostCIDRs %q", c["hostCIDRs"])
					}
					if c["storageEngine"] != "wiredTiger" || c["cacheSizeGB"] != "1" {
						t.Errorf("storageEngine %v, cacheSizeGB %v: want the first choices", c["storageEngine"], c["cacheSizeGB"])
					}
					if _, sent := c["journalMode"]; sent {
						t.Error("journalMode sent for another storage engine")
					}
					return nil
				},
			},
			{Config: forget(f, "mtncloud_mongodb.db")},
		},
	})
}

// An order whose workflow succeeds but whose VM fails is a failed apply (the resource is tainted,
// so the next apply orders again), and a VM that fails later plans a replace.
func TestOrder_FailedInstance(t *testing.T) {
	f, s, insts := orderFakes(t)
	made := s.made
	failNext := true
	s.made = func(item map[string]any) {
		made(item)
		if failNext {
			insts.mu.Lock()
			for _, in := range insts.insts {
				in["status"], in["statusMessage"] = "failed", "Image virtual size is 40GB and doesn't fit in a volume of size 10GB"
			}
			insts.mu.Unlock()
		}
	}
	cfg := f.providerConfig() + `
resource "mtncloud_mysql" "db" {
  name             = "db-1"
  group            = "MTNNG_CLOUD_AZ_1"
  project          = "web-prod-Acme"
  plan             = "G2S4"
  root_volume_size = 40
  networks         = ["app-net"]
}
`
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories(),
		Steps: []resource.TestStep{
			{Config: cfg, ExpectError: regexp.MustCompile(wrapped(`(?s)MTN MySQL instance failed.*"failed": Image virtual size is 40GB`))},
			{
				// Tainted: the next apply deletes the failed VM with the order and orders again.
				PreConfig: func() { failNext = false },
				Config:    cfg,
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction("mtncloud_mysql.db", plancheck.ResourceActionDestroyBeforeCreate)}},
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue("mtncloud_mysql.db", tfjsonpath.New("instance_statuses"),
						knownvalue.MapExact(map[string]knownvalue.Check{"9702": knownvalue.StringExact("running")})),
				},
			},
			{Config: cfg, ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()}}},
			{
				// An empty map where nothing was set is the same order: no replace.
				Config: strings.Replace(cfg, `networks         = ["app-net"]`, "networks         = [\"app-net\"]\n  extra_inputs     = {}", 1),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction("mtncloud_mysql.db", plancheck.ResourceActionUpdate)}},
			},
			{
				// The VM fails after the order: refresh sees it, and the plan orders again.
				PreConfig: func() {
					insts.mu.Lock()
					for _, in := range insts.insts {
						in["status"] = "failed"
					}
					insts.mu.Unlock()
				},
				Config: cfg, PlanOnly: true, ExpectNonEmptyPlan: true,
				ConfigPlanChecks: resource.ConfigPlanChecks{PostApplyPostRefresh: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction("mtncloud_mysql.db", plancheck.ResourceActionReplace)}},
			},
			{Config: forget(f, "mtncloud_mysql.db")},
		},
	})
	if !strings.Contains(strings.Join(s.deleted, ","), "item 9801") {
		t.Errorf("the failed order wasn't deleted before ordering again: %v", s.deleted)
	}
}

// wrapped lets a pattern's spaces match Terraform's line wrapping of error text.
func wrapped(pattern string) string { return strings.ReplaceAll(pattern, " ", `\s+`) }

// An app order (managed services): the workflow ends once it has asked for the app, whose VMs are
// named from MTN's template, not after the order (QA, 2026-10-31). The apply waits for the app and
// takes its VMs; a failed workflow's own reason is in the error; destroy deletes the app.
func TestAppOrder(t *testing.T) {
	f, s, insts := orderFakes(t)
	s.apps, s.appName, s.appStep = map[int64][]int64{}, map[int64]string{}, map[int64][]string{}
	s.insts = insts
	// Another project's app with the same name: never taken for this order's, never deleted.
	insts.insts[9690] = map[string]any{"id": 9690, "name": "ab-42-MTN-CLOUD-KAFKA-7", "status": "running",
		"config": map[string]any{"resourcePoolId": "pool-9999"}}
	s.apps[9900], s.appName[9900] = []int64{9690}, "kafka-1"
	s.made = func(item map[string]any) {
		cfg, _ := item["config"].(map[string]any)
		insts.mu.Lock()
		defer insts.mu.Unlock()
		insts.nextID++
		insts.insts[insts.nextID] = map[string]any{"id": insts.nextID, "name": "ab-42-MTN-CLOUD-KAFKA-1", "status": "running",
			"config": map[string]any{"resourcePoolId": cfg["resourcepool"]}}
		s.apps[9901] = []int64{insts.nextID}
		s.appName[9901], s.appStep[9901] = "kafka-1", []string{"pending", "pending", "running"}
	}
	cfg := f.providerConfig() + `
resource "mtncloud_managed_streaming" "k" {
  name             = "kafka-1"
  group            = "MTNNG_CLOUD_AZ_1"
  project          = "web-prod-Acme"
  plan             = "G2S4"
  root_volume_size = 100
  networks         = ["app-net"]
  security_groups  = ["web"]
  admin_password   = "s3cret-value"
}
`
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories(),
		Steps: []resource.TestStep{
			{
				PreConfig: func() {
					s.result = "error"
					s.failure = "Validating inputs\nError: kafkaVersion must be 4.3.1 or 4.2.1 (got: 'null').\nNothing was provisioned."
				},
				Config:      cfg,
				ExpectError: regexp.MustCompile(wrapped(`(?s)order failed.*The workflow says: .*kafkaVersion must be 4.3.1 or 4.2.1 \(got: 'null'\). Nothing was provisioned. Check it`)),
			},
			{
				PreConfig: func() { s.result, s.failure = "success", "" },
				Config:    cfg,
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue("mtncloud_managed_streaming.k", tfjsonpath.New("instance_statuses"),
						knownvalue.MapExact(map[string]knownvalue.Check{"9701": knownvalue.StringExact("running")})),
				},
				Check: func(*terraform.State) error {
					s.mu.Lock()
					defer s.mu.Unlock()
					// A list that can't be blank sends its first choice, as the console does.
					if c, _ := s.sent[9802]["config"].(map[string]any); c["kafkaVersion"] != "4.3.1" {
						t.Errorf("kafkaVersion %v, want the form's first choice", c["kafkaVersion"])
					}
					if len(s.appStep[9901]) != 1 {
						t.Errorf("app statuses left %v: the apply didn't wait for it", s.appStep[9901])
					}
					return nil
				},
			},
			{Config: cfg, ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()}}},
		},
	})
	if d := strings.Join(s.deleted, ","); !strings.Contains(d, "app 9901") || strings.Contains(d, "app 9900") {
		t.Errorf("destroy deleted %v: want this order's app 9901, never the other project's 9900", s.deleted)
	}
}

// An order's app renamed in the console, with an instance someone else added to it: the order keeps
// its app by ID, plans nothing, and destroy takes the added instance out of the app (left running)
// before deleting the app with the order's own instance (B-028 catches 1 and 2).
func TestAppOrder_RenamedAppAddedInstance(t *testing.T) {
	f, s, insts := orderFakes(t)
	s.apps, s.appName, s.appStep = map[int64][]int64{}, map[int64]string{}, map[int64][]string{}
	s.insts = insts
	s.made = func(item map[string]any) {
		cfg, _ := item["config"].(map[string]any)
		insts.mu.Lock()
		defer insts.mu.Unlock()
		insts.nextID++
		insts.insts[insts.nextID] = map[string]any{"id": insts.nextID, "name": "ab-42-MTN-CLOUD-KAFKA-1", "status": "running",
			"config": map[string]any{"resourcePoolId": cfg["resourcepool"]}}
		s.apps[9901], s.appName[9901] = []int64{insts.nextID}, "kafka-1"
	}
	cfg := f.providerConfig() + `
resource "mtncloud_managed_streaming" "k" {
  name             = "kafka-1"
  group            = "MTNNG_CLOUD_AZ_1"
  project          = "web-prod-Acme"
  plan             = "G2S4"
  root_volume_size = 100
  networks         = ["app-net"]
  security_groups  = ["web"]
  admin_password   = "s3cret-value"
}
`
	const res = "mtncloud_managed_streaming.k"
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories(),
		CheckDestroy: func(*terraform.State) error {
			s.mu.Lock()
			defer s.mu.Unlock()
			if d := strings.Join(s.deleted, ","); !strings.Contains(d, "out of app 9901: 9750") || !strings.Contains(d, "app 9901") {
				t.Errorf("destroy did %v: want instance 9750 taken out of app 9901, then the app deleted", s.deleted)
			}
			insts.mu.Lock()
			defer insts.mu.Unlock()
			if _, ok := insts.insts[9750]; !ok {
				t.Error("the instance added to the app was deleted with the order")
			}
			if _, ok := insts.insts[9701]; ok {
				t.Error("the order's own instance 9701 wasn't deleted")
			}
			return nil
		},
		Steps: []resource.TestStep{
			{Config: cfg, ConfigStateChecks: []statecheck.StateCheck{
				statecheck.ExpectKnownValue(res, tfjsonpath.New("app_id"), knownvalue.StringExact("9901")),
			}},
			{
				PreConfig: func() {
					s.mu.Lock()
					s.appName[9901] = "streaming-renamed"
					s.apps[9901] = append(s.apps[9901], 9750)
					s.mu.Unlock()
					insts.mu.Lock()
					insts.insts[9750] = map[string]any{"id": 9750, "name": "worker-x", "status": "running",
						"config": map[string]any{"resourcePoolId": "pool-9201"}}
					insts.mu.Unlock()
				},
				Config:           cfg,
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()}},
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(res, tfjsonpath.New("instance_ids"), knownvalue.ListExact([]knownvalue.Check{knownvalue.StringExact("9701")})),
				},
			},
		},
	})
}

// An app order imported after an instance was added to its app: the import has no saved instances,
// but the added one isn't taken for the order's, so destroy takes it out of the app, left running.
func TestAppOrder_ImportedAddedInstance(t *testing.T) {
	f, s, insts := orderFakes(t)
	s.apps, s.appName, s.appStep = map[int64][]int64{}, map[int64]string{}, map[int64][]string{}
	s.insts = insts
	s.made = func(item map[string]any) {
		cfg, _ := item["config"].(map[string]any)
		insts.mu.Lock()
		defer insts.mu.Unlock()
		insts.nextID++
		insts.insts[insts.nextID] = map[string]any{"id": insts.nextID, "name": "ab-42-MTN-CLOUD-KAFKA-1", "status": "running",
			"config": map[string]any{"resourcePoolId": cfg["resourcepool"]}}
		s.apps[9901], s.appName[9901] = []int64{insts.nextID}, "kafka-1"
	}
	cfg := f.providerConfig() + `
resource "mtncloud_managed_streaming" "k" {
  name             = "kafka-1"
  group            = "MTNNG_CLOUD_AZ_1"
  project          = "web-prod-Acme"
  plan             = "G2S4"
  root_volume_size = 100
  networks         = ["app-net"]
  security_groups  = ["web"]
  admin_password   = "s3cret-value"
}
`
	const res = "mtncloud_managed_streaming.k"
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories(),
		CheckDestroy: func(*terraform.State) error {
			insts.mu.Lock()
			defer insts.mu.Unlock()
			if _, ok := insts.insts[9750]; !ok {
				t.Errorf("the instance added to the app was deleted with the imported order (%v)", s.deleted)
			}
			if _, ok := insts.insts[9701]; ok {
				t.Error("the order's own instance 9701 wasn't deleted")
			}
			return nil
		},
		Steps: []resource.TestStep{
			{Config: cfg},
			// Forgotten by Terraform, left in the cloud: then imported fresh, with no saved instances.
			{Config: f.providerConfig() + `
removed {
  from = mtncloud_managed_streaming.k
  lifecycle {
    destroy = false
  }
}
`},
			{
				PreConfig: func() {
					s.mu.Lock()
					s.apps[9901] = append(s.apps[9901], 9750)
					s.mu.Unlock()
					insts.mu.Lock()
					insts.insts[9750] = map[string]any{"id": 9750, "name": "worker-x", "status": "running",
						"config": map[string]any{"resourcePoolId": "pool-9201"}}
					insts.mu.Unlock()
				},
				Config: cfg, ResourceName: res, ImportState: true, ImportStateId: "9801", ImportStatePersist: true,
			},
			{Config: cfg, ConfigStateChecks: []statecheck.StateCheck{
				statecheck.ExpectKnownValue(res, tfjsonpath.New("instance_ids"), knownvalue.ListExact([]knownvalue.Check{knownvalue.StringExact("9701")})),
			}},
		},
	})
}

// An app that settles in any state but running fails the apply, so the next apply orders again.
func TestAppOrder_AppFails(t *testing.T) {
	f, s, insts := orderFakes(t)
	s.apps, s.appName, s.appStep = map[int64][]int64{}, map[int64]string{}, map[int64][]string{}
	s.insts = insts
	s.made = func(item map[string]any) {
		cfg, _ := item["config"].(map[string]any)
		insts.mu.Lock()
		defer insts.mu.Unlock()
		insts.nextID++
		insts.insts[insts.nextID] = map[string]any{"id": insts.nextID, "name": "ab-42-MTN-CLOUD-KAFKA-1", "status": "failed",
			"config": map[string]any{"resourcePoolId": cfg["resourcepool"]}}
		s.apps[9901], s.appName[9901], s.appStep[9901] = []int64{insts.nextID}, "kafka-1", []string{"pending", "error"}
	}
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories(),
		Steps: []resource.TestStep{{
			Config: f.providerConfig() + `
resource "mtncloud_managed_streaming" "k" {
  name             = "kafka-1"
  group            = "MTNNG_CLOUD_AZ_1"
  project          = "web-prod-Acme"
  plan             = "G2S4"
  root_volume_size = 100
  networks         = ["app-net"]
  security_groups  = ["web"]
  admin_password   = "s3cret-value"
}
`,
			ExpectError: regexp.MustCompile(wrapped(`(?s)app failed.*App "kafka-1" \(9901\) from order 9801 is "error"`)),
		}},
	})
}

// A workflow that succeeds without a VM the order can find (SOPHOS names its VM itself, QA
// 2026-10-31) fails the apply at the create timeout instead of succeeding with nothing.
func TestOrder_NoInstance(t *testing.T) {
	f, s, _ := orderFakes(t)
	s.made = func(map[string]any) {} // the workflow makes nothing named after the order
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories(),
		Steps: []resource.TestStep{{
			Config: f.providerConfig() + `
resource "mtncloud_mysql" "db" {
  name             = "db-1"
  group            = "MTNNG_CLOUD_AZ_1"
  project          = "web-prod-Acme"
  plan             = "G2S4"
  root_volume_size = 40
  networks         = ["app-net"]
  timeouts {
    create = "5s"
  }
}
`,
			ExpectError: regexp.MustCompile(wrapped(`(?s)instance not found.*no instance named "db-1" is in the project.*named it itself`)),
		}, {Config: forget(f, "mtncloud_mysql.db")}},
	})
}

// SOPHOS: MTN's workflow names the VM itself and reports it in its output. The order takes the VM
// it reported, in its project only (never a same-name VM elsewhere, nor one named after the
// order), and destroy deletes that VM.
func TestSophosOrder(t *testing.T) {
	f, s, insts := orderFakes(t)
	f.net.(*netStore).networks[9351] = map[string]any{"id": 9351, "name": "dmz-net", "status": "ACTIVE",
		"zonePool": map[string]any{"id": 9201, "name": "web-prod-Acme"}}
	const vm = "ab-42-MTN-CLOUD-SPH001-1"
	insts.insts[4600] = map[string]any{"id": 4600, "name": vm, "status": "running", "config": map[string]any{"resourcePoolId": "pool-9999"}}
	insts.insts[4601] = map[string]any{"id": 4601, "name": "fw-1", "status": "running", "config": map[string]any{"resourcePoolId": "pool-9201"}}
	s.made = func(item map[string]any) {
		cfg, _ := item["config"].(map[string]any)
		insts.mu.Lock()
		defer insts.mu.Unlock()
		insts.insts[4639] = map[string]any{"id": 4639, "name": vm, "status": "running", "config": map[string]any{"resourcePoolId": cfg["resourcepool"]}}
		s.output = "Creating stack ...\n Instance name : " + vm + "\n   Console          : https://console.example/provisioning/instances/4639\n"
	}
	cfg := f.providerConfig() + `
resource "mtncloud_sophos_firewall" "fw" {
  name             = "fw-1"
  group            = "MTNNG_CLOUD_AZ_1"
  project          = "web-prod-Acme"
  plan             = "G2S4"
  root_volume_size = 80
  networks         = ["app-net", "dmz-net"]
  floating_ip_pool = "public-network-01"
}
`
	noName := strings.Replace(cfg, "  name             = \"fw-1\"\n", "", 1)
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories(),
		CheckDestroy: func(*terraform.State) error {
			insts.mu.Lock()
			defer insts.mu.Unlock()
			if _, ok := insts.insts[4639]; ok {
				t.Error("the reported VM 4639 wasn't deleted")
			}
			for _, id := range []int64{4600, 4601} {
				if _, ok := insts.insts[id]; !ok {
					t.Errorf("instance %d deleted, but the workflow didn't report it", id)
				}
			}
			return nil
		},
		Steps: []resource.TestStep{
			{Config: cfg, ConfigStateChecks: []statecheck.StateCheck{
				statecheck.ExpectKnownValue("mtncloud_sophos_firewall.fw", tfjsonpath.New("instance_ids"),
					knownvalue.ListExact([]knownvalue.Check{knownvalue.StringExact("4639")})),
			}},
			{Config: cfg, ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()}}},
			// name isn't used: removing it changes nothing, rather than ordering a new firewall.
			{Config: noName, ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
				plancheck.ExpectResourceAction("mtncloud_sophos_firewall.fw", plancheck.ResourceActionUpdate)}}},
			{Config: noName, ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()}}},
			// An order whose apply was cut off is imported by its inventory ID, and finds its VM again.
			{Config: forget(f, "mtncloud_sophos_firewall.fw")},
			{Config: noName, ResourceName: "mtncloud_sophos_firewall.fw", ImportState: true, ImportStateId: "9801", ImportStatePersist: true},
			{Config: noName, ConfigStateChecks: []statecheck.StateCheck{
				statecheck.ExpectKnownValue("mtncloud_sophos_firewall.fw", tfjsonpath.New("instance_ids"),
					knownvalue.ListExact([]knownvalue.Check{knownvalue.StringExact("4639")})),
			}},
			{Config: noName, ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()}}},
		},
	})
}

// A workflow that fails after its VM is up (MTN's SOPHOS workflow, QA 2026-10-31) keeps the VM in
// state: the apply fails and taints it, so the next apply deletes the VM before ordering again; and
// an order that is seen to fail later, with its VM still there, plans a replace instead of leaving
// the VM behind.
func TestOrder_FailedAfterVM(t *testing.T) {
	f, s, insts := orderFakes(t)
	lock := func(fn func()) func() { return func() { s.mu.Lock(); defer s.mu.Unlock(); fn() } }
	s.result, s.madeOnError = "error", true
	cfg := f.providerConfig() + `
resource "mtncloud_mysql" "db" {
  name             = "db-1"
  group            = "MTNNG_CLOUD_AZ_1"
  project          = "web-prod-Acme"
  plan             = "G2S4"
  root_volume_size = 40
  networks         = ["app-net"]
}
`
	vms := func(want int) resource.TestCheckFunc {
		return func(*terraform.State) error {
			if n := insts.count(); n != want {
				t.Errorf("%d VMs, want %d", n, want)
			}
			return nil
		}
	}
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories(),
		CheckDestroy:             func(*terraform.State) error { return vms(0)(nil) },
		Steps: []resource.TestStep{
			{Config: cfg, ExpectError: regexp.MustCompile(wrapped(`(?s)order failed.*left instance 9701, kept in state`))},
			{
				PreConfig: lock(func() { s.result, s.madeOnError = "success", false }),
				Config:    cfg,
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction("mtncloud_mysql.db", plancheck.ResourceActionDestroyBeforeCreate)}},
				Check: vms(1), // the failed order's VM was deleted, the new one made
			},
			{Config: cfg, ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()}}},
			{
				// The order is seen to have failed later, its VM still there: replaced, not dropped.
				PreConfig: lock(func() {
					for _, it := range s.items {
						if ex, ok := it["execution"].(map[string]any); ok && it["id"] != 9795 {
							ex["status"] = "failed"
						}
					}
				}),
				Config: cfg, PlanOnly: true, ExpectNonEmptyPlan: true,
				ConfigPlanChecks: resource.ConfigPlanChecks{PostApplyPostRefresh: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction("mtncloud_mysql.db", plancheck.ResourceActionReplace)}},
			},
		},
	})
}
