package provider

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

const lookupOrder = `resource "mtncloud_mysql" "db" {
  name              = "db-1"
  group             = "MTNNG_CLOUD_AZ_1"
  project           = "web-prod-Acme"
  plan              = "G2S4"
  root_volume_size  = 40
  networks          = ["app-net"]
  database_name     = "shop"
  database_user     = "shop"
  database_password = "s3cret-value"
}
`

// An instance the list can't decode doesn't hide the order's VM: it is asked for by name.
func TestOrder_UnreadableListFindsVMByName(t *testing.T) {
	f, _, insts := orderFakes(t)
	insts.listUnreadable = true
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories(),
		Steps: []resource.TestStep{{
			Config: f.providerConfig() + lookupOrder,
			Check:  resource.TestCheckResourceAttr("mtncloud_mysql.db", "instance_ids.#", "1"),
		}},
	})
}

// The workflow worked and only finding the VM failed: the apply succeeds, the order stays in state
// untainted, nothing is ordered again, and the next refresh finds the VM.
func TestOrder_LookupFailureKeepsTheOrder(t *testing.T) {
	f, s, insts := orderFakes(t)
	insts.listFails = true
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories(),
		Steps: []resource.TestStep{
			{
				Config: f.providerConfig() + lookupOrder,
				Check:  resource.TestCheckResourceAttr("mtncloud_mysql.db", "instance_ids.#", "0"),
			},
			{
				PreConfig: func() {
					insts.mu.Lock()
					defer insts.mu.Unlock()
					insts.listFails = false
				},
				Config:           f.providerConfig() + lookupOrder,
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()}},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("mtncloud_mysql.db", "instance_ids.#", "1"),
					func(*terraform.State) error {
						s.mu.Lock()
						defer s.mu.Unlock()
						if len(s.sent) != 1 {
							t.Errorf("ordered %d times, want once", len(s.sent))
						}
						return nil
					},
				),
			},
		},
	})
}

// The VM is found, but reading it fails: the order stays, and the next refresh reads its status.
func TestOrder_InstanceReadFailureKeepsTheOrder(t *testing.T) {
	f, _, insts := orderFakes(t)
	insts.getFails = 1
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories(),
		Steps: []resource.TestStep{
			{
				Config: f.providerConfig() + lookupOrder,
				Check:  resource.TestCheckResourceAttr("mtncloud_mysql.db", "instance_ids.#", "1"),
			},
			{
				RefreshState: true,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("mtncloud_mysql.db", "instance_names.0", "db-1"),
					resource.TestCheckResourceAttr("mtncloud_mysql.db", "instance_statuses.%", "1"),
				),
			},
		},
	})
}

// A reported VM has no name to ask for: with the list unreadable it is read by the ID its workflow printed.
func TestOrder_UnreadableListFindsReportedVM(t *testing.T) {
	f, s, insts := orderFakes(t)
	insts.listUnreadable = true
	s.made = func(item map[string]any) {
		cfg, _ := item["config"].(map[string]any)
		insts.mu.Lock()
		defer insts.mu.Unlock()
		insts.insts[4650] = map[string]any{"id": 4650, "name": "ab-42-MTN-CLOUD-VPN-1", "status": "running",
			"config": map[string]any{"resourcePoolId": cfg["resourcepool"]}}
		s.output = "Console : https://console.example/provisioning/instances/4650\n"
	}
	f.net.(*netStore).networks[9351] = map[string]any{"id": 9351, "name": "dmz-net", "status": "ACTIVE",
		"zonePool": map[string]any{"id": 9201, "name": "web-prod-Acme"}}
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories(),
		Steps: []resource.TestStep{{
			Config: f.providerConfig() + `
resource "mtncloud_pfsense_vpn" "vpn" {
  group            = "MTNNG_CLOUD_AZ_1"
  project          = "web-prod-Acme"
  plan             = "G2S4"
  networks         = ["app-net", "dmz-net"]
  floating_ip_pool = "public-network-01"
}
`,
			Check: resource.TestCheckResourceAttr("mtncloud_pfsense_vpn.vpn", "instance_ids.0", "4650"),
		}},
	})
}

// The app can't be read when the workflow ends: the order stays, the next refresh takes the app
// that holds its VM, never another project's with the same name, and a failed one is replaced.
func TestAppOrder_LookupFailure(t *testing.T) {
	const cfg = `
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
	const addr = "mtncloud_managed_streaming.k"
	setup := func(t *testing.T, status string) (*fakeAPI, *orderStore) {
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
			s.apps[9901], s.appName[9901], s.appStep[9901] = []int64{insts.nextID}, "kafka-1", []string{status}
			// Someone else's, in another project, newer: it must never be taken.
			insts.insts[8800] = map[string]any{"id": 8800, "name": "other", "status": "running", "config": map[string]any{"resourcePoolId": "pool-9999"}}
			s.apps[9902], s.appName[9902], s.appStep[9902] = []int64{8800}, "kafka-1", []string{"failed"}
			s.appsFail = true
		}
		return f, s
	}
	clear := func(s *orderStore) func() {
		return func() {
			s.mu.Lock()
			defer s.mu.Unlock()
			s.appsFail = false
		}
	}
	t.Run("app came up", func(t *testing.T) {
		f, s := setup(t, "running")
		resource.UnitTest(t, resource.TestCase{
			ProtoV6ProviderFactories: factories(),
			Steps: []resource.TestStep{
				{Config: f.providerConfig() + cfg, Check: resource.TestCheckNoResourceAttr(addr, "app_id")},
				{PreConfig: clear(s), RefreshState: true, Check: resource.TestCheckResourceAttr(addr, "app_id", "9901")},
				// Checked once: a later change to the app is left alone.
				{
					PreConfig: func() {
						s.mu.Lock()
						defer s.mu.Unlock()
						s.appStep[9901] = []string{"failed"}
					},
					Config:           f.providerConfig() + cfg,
					ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()}},
				},
			},
		})
	})
	for _, status := range []string{"failed", "warning"} {
		t.Run("app "+status, func(t *testing.T) {
			f, s := setup(t, status)
			resource.UnitTest(t, resource.TestCase{
				ProtoV6ProviderFactories: factories(),
				Steps: []resource.TestStep{
					{Config: f.providerConfig() + cfg, Check: resource.TestCheckNoResourceAttr(addr, "app_id")},
					{
						PreConfig: clear(s), RefreshState: true, ExpectNonEmptyPlan: true,
						Check: resource.ComposeAggregateTestCheckFunc(
							resource.TestCheckResourceAttr(addr, "app_id", "9901"),
							resource.TestCheckResourceAttr(addr, "instance_statuses.app-9901", "failed"),
							resource.TestCheckNoResourceAttr(addr, "instance_statuses.app-9902"),
						),
					},
					{Config: f.providerConfig() + cfg, PlanOnly: true, ExpectNonEmptyPlan: true},
				},
			})
		})
	}
}

// The plan list answers 500: "Invalid plan" would send the user looking for a typo in a valid plan.
func TestOrder_PlanLookupFailureDoesNotBlameThePlan(t *testing.T) {
	f, _, _ := orderFakes(t)
	f.extra = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("optionTypeId") == "7" {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		body, err := os.ReadFile(filepath.Join("..", "client", "testdata", "lookups", "options_list_"+r.URL.Query().Get("optionTypeId")+".json"))
		if err != nil {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write(body)
	})
	f.extraFor = func(p string) bool { return p == "/api/options/list" }
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories(),
		Steps: []resource.TestStep{{Config: f.providerConfig() + lookupOrder, PlanOnly: true,
			ExpectError: regexp.MustCompile(`Couldn't check the plan[\s\S]*wasn't\s+checked`)}},
	})
}

// The console can't be reached: no heading blames the group or the project.
func TestLookupFailure_DoesNotBlameTheValue(t *testing.T) {
	down := httptest.NewServer(http.NotFoundHandler())
	url := down.URL
	down.Close()
	provider := `
provider "mtncloud" {
  url      = "` + url + `"
  username = "someone"
  password = "not-a-real-password"
  project  = "testproject"
}
`
	for name, tc := range map[string]struct{ config, want string }{
		"project": {`resource "mtncloud_security_group" "sg" {
  name = "x"
}
`, `Couldn't check the project[\s\S]*wasn't\s+checked[\s\S]*login\s+to\s+MTN\s+Cloud\s+failed`},
		"group": {`resource "mtncloud_mariadb" "db" {
  name             = "x"
  version          = "11.8"
  plan             = "G2S4"
  root_volume_size = 50
  networks         = ["n"]
  allowed_cidrs    = ["10.0.0.0/24"]
}
`, `Couldn't check the group[\s\S]*login\s+to\s+MTN\s+Cloud\s+failed`},
	} {
		t.Run(name, func(t *testing.T) {
			resource.UnitTest(t, resource.TestCase{
				ProtoV6ProviderFactories: factories(),
				Steps:                    []resource.TestStep{{Config: provider + tc.config, PlanOnly: true, ExpectError: regexp.MustCompile(tc.want)}},
			})
		})
	}
}
