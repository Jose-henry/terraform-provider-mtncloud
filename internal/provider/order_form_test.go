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
  extra_inputs     = { newField = "x" }
`),
				Check: func(*terraform.State) error {
					s.mu.Lock()
					defer s.mu.Unlock()
					c, _ := s.sent[9801]["config"].(map[string]any)
					if c["newField"] != "x" || c["adminUsername"] != "Admin" || c["mongoUserRole"] != "readWrite" || c["enableAuth"] != false {
						t.Errorf("newField %v, adminUsername %v, mongoUserRole %v, enableAuth %v", c["newField"], c["adminUsername"], c["mongoUserRole"], c["enableAuth"])
					}
					if _, sent := c["replicaSetName"]; sent {
						t.Error("replicaSetName sent without initReplicaSet")
					}
					if _, sent := c["adminPassword"]; sent {
						t.Error("adminPassword sent")
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
