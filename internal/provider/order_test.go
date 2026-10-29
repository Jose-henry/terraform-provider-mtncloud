package provider

import (
	"encoding/json"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/knownvalue"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/statecheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"
)

// orderFakes wires the order, instance and network fakes: a VM order's workflow creates an
// instance named after the order in its project, as MTN's scripts do.
func orderFakes(t *testing.T) (*fakeAPI, *orderStore, *instStore) {
	t.Helper()
	f, s := newOrderFakes(t)
	insts, nets := newInstStore(), newNetStore()
	nets.networks[9350] = map[string]any{"id": 9350, "name": "app-net", "status": "ACTIVE",
		"zonePool": map[string]any{"id": 9201, "name": "web-prod-Acme"}}
	f.inst, f.net = insts, nets
	s.made = func(item map[string]any) {
		cfg, _ := item["config"].(map[string]any)
		name, _ := cfg["name"].(string)
		if name == "" {
			return
		}
		insts.mu.Lock()
		defer insts.mu.Unlock()
		insts.nextID++
		insts.insts[insts.nextID] = map[string]any{"id": insts.nextID, "name": name, "status": "running",
			"config": map[string]any{"resourcePoolId": cfg["resourcepool"]}}
	}
	return f, s, insts
}

func TestMySQLOrder(t *testing.T) {
	f, s, insts := orderFakes(t)
	body := `
  name                 = "db-1"
  group                = "MTNNG_CLOUD_AZ_1"
  project              = "web-prod-Acme"
  environment          = "Production"
  labels               = ["db"]
  plan                 = "G2S4"
  root_volume_size     = 40
  networks             = ["app-net"]
  security_groups      = ["web"]
  floating_ip_pool     = "public-network-01"
  version              = "8.4 LTS"
  database_name        = "shop"
  database_user        = "shop"
  database_password    = "s3cret-value"
  password_expiry_days = 90

  data_volume {
    size      = 100
    datastore = "ENCR_AT_REST"
  }
`
	cfg := func(b string) string {
		return f.providerConfig() + "resource \"mtncloud_mysql\" \"db\" {" + b + "}\n"
	}
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories(),
		Steps: []resource.TestStep{
			{
				Config: cfg(body),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue("mtncloud_mysql.db", tfjsonpath.New("id"), knownvalue.StringExact("9801")),
					statecheck.ExpectKnownValue("mtncloud_mysql.db", tfjsonpath.New("status"), knownvalue.StringExact("success")),
					statecheck.ExpectKnownValue("mtncloud_mysql.db", tfjsonpath.New("instance_ids"), knownvalue.ListExact([]knownvalue.Check{knownvalue.StringExact("9701")})),
					statecheck.ExpectKnownValue("mtncloud_mysql.db", tfjsonpath.New("database_password"), knownvalue.Null()),
				},
				Check: func(*terraform.State) error {
					s.mu.Lock()
					defer s.mu.Unlock()
					got, _ := json.Marshal(s.sent[9801]["config"])
					want := `{"cloud":"4","environment":"production","floatingip":"pool-44","group-vpn":"9101","hostCIDRs":"0.0.0.0/0",` +
						`"labels":"db","makeUserDbOwner":false,"mysqlVersion":"8.4","name":"db-1",` +
						`"networks":[{"network":{"id":"network-9350"},"primaryInterface":true}],"passwordExpiryDays":90,"plan":13107,` +
						`"resourcepool":"pool-9201","securitygroups":[{"id":9301},{"id":9302}],` +
						`"volumes":[{"name":"root","rootVolume":true,"size":40},{"name":"data-1","rootVolume":false,"size":100,"storageType":414}],` +
						`"dbName":"shop","dbPassword":"s3cret-value","dbUsername":"shop"}`
					var a, b map[string]any
					_ = json.Unmarshal(got, &a)
					_ = json.Unmarshal([]byte(want), &b)
					ga, _ := json.Marshal(a)
					gb, _ := json.Marshal(b)
					if string(ga) != string(gb) {
						t.Errorf("order config\n got %s\nwant %s", ga, gb)
					}
					return nil
				},
			},
			{Config: cfg(body), ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()}}},
			// An ordered item can't change; a secret isn't stored, so changing it shows nothing.
			{Config: cfg(strings.Replace(body, `"G2S4"`, `"G1S1"`, 1)), PlanOnly: true,
				ExpectError: regexp.MustCompile(`(?s)Can't change an ordered MTN MySQL.*plan`)},
			{Config: cfg(strings.Replace(body, "s3cret-value", "other", 1)), PlanOnly: true},
			// Import by order ID: the configuration is adopted, nothing is ordered again.
			{Config: cfg(body), ResourceName: "mtncloud_mysql.db", ImportState: true, ImportStateId: "9801",
				ImportStateCheck: func(s []*terraform.InstanceState) error {
					// The inventory has no name to find instances by: the adopting update finds them.
					if len(s) != 1 || s[0].Attributes["instance_ids.#"] != "0" || s[0].Attributes["status"] != "success" {
						t.Errorf("imported: %v", s)
					}
					return nil
				}},
			{ResourceName: "mtncloud_mysql.db", ImportState: true, ImportStateId: "9795", Config: cfg(body),
				ExpectError: regexp.MustCompile(`is a "Create Project", not a "MTN MySQL"`)},
			{
				// Its instance deleted in the console: the order is planned again (and the test's
				// final destroy finds nothing to do).
				PreConfig: func() {
					insts.mu.Lock()
					clear(insts.insts)
					insts.mu.Unlock()
				},
				Config: cfg(body), PlanOnly: true, ExpectNonEmptyPlan: true,
			},
			{Config: forget(f, "mtncloud_mysql.db")},
		},
	})
	if s.orders != 1 {
		t.Errorf("%d orders placed, want 1", s.orders)
	}
}

// Destroy deletes what the order made, then its inventory record: the instance directly, or the
// app holding it (with its instances).
func TestOrder_Destroy(t *testing.T) {
	for name, tc := range map[string]struct {
		apps map[int64][]int64
		want []string
	}{
		"instance":      {want: []string{"item 9801"}},
		"app":           {apps: map[int64][]int64{9901: {9701}}, want: []string{"app 9901", "item 9801"}},
		"another app's": {apps: map[int64][]int64{9902: {9750}}, want: []string{"item 9801"}},
		// The app also holds the user's own instance 9750: it is kept, and only 9701 is deleted.
		"shared app": {apps: map[int64][]int64{9903: {9701, 9750}}, want: []string{"item 9801"}},
	} {
		t.Run(name, func(t *testing.T) {
			f, s, insts := orderFakes(t)
			s.insts = insts
			cfg := f.providerConfig() + `resource "mtncloud_mysql" "db" {
  name             = "db-1"
  group            = "MTNNG_CLOUD_AZ_1"
  project          = "web-prod-Acme"
  plan             = "G2S4"
  root_volume_size = 40
  networks         = ["app-net"]
  database_name    = "shop"
  database_user    = "shop"
  database_password = "s3cret-value"
  timeouts {
    delete = "20s"
  }
}
`
			resource.UnitTest(t, resource.TestCase{
				ProtoV6ProviderFactories: factories(),
				Steps: []resource.TestStep{
					{Config: cfg},
					{
						PreConfig: func() { s.mu.Lock(); s.apps = tc.apps; s.mu.Unlock() },
						Config:    f.providerConfig(),
						Check: func(*terraform.State) error {
							s.mu.Lock()
							defer s.mu.Unlock()
							insts.mu.Lock()
							defer insts.mu.Unlock()
							if !slices.Equal(s.deleted, tc.want) {
								t.Errorf("deleted %v, want %v", s.deleted, tc.want)
							}
							if _, ok := insts.insts[9701]; ok {
								t.Error("the order's instance is still there")
							}
							return nil
						},
					},
				},
			})
		})
	}
}

// An order whose record has no execution at all (an older order) is destroyed without waiting
// for one; with nothing named after it left, destroy refuses and deletes nothing.
func TestOrder_DestroyEdges(t *testing.T) {
	for name, tc := range map[string]struct {
		prep     func(s *orderStore, insts *instStore)
		imported bool
		wantErr  string
		want     []string
	}{
		"no execution": {
			prep: func(s *orderStore, _ *instStore) {
				s.items[9801]["execution"] = nil
				s.result = ""
			},
			want: []string{"item 9801"},
		},
		// The saved instance was renamed, so it no longer matches the order's name: it is left alone,
		// and with nothing else found, destroy refuses.
		// Imported and not applied since: the order's name isn't known, so nothing is deleted.
		"imported, not applied": {
			prep:     func(*orderStore, *instStore) {},
			imported: true,
			wantErr:  "imported and not applied",
		},
		"saved instance renamed": {
			prep:    func(_ *orderStore, insts *instStore) { insts.insts[9701]["name"] = "db-1-2" },
			wantErr: "Nothing it made was found",
		},
	} {
		t.Run(name, func(t *testing.T) {
			f, s, insts := orderFakes(t)
			s.insts = insts
			cfg := f.providerConfig() + `resource "mtncloud_mysql" "db" {
  name             = "db-1"
  group            = "MTNNG_CLOUD_AZ_1"
  project          = "web-prod-Acme"
  plan             = "G2S4"
  root_volume_size = 40
  networks         = ["app-net"]
  database_name    = "shop"
  database_user    = "shop"
  database_password = "s3cret-value"
  timeouts {
    delete = "20s"
  }
}
`
			destroy := resource.TestStep{
				PreConfig: func() {
					s.mu.Lock()
					insts.mu.Lock()
					tc.prep(s, insts)
					insts.mu.Unlock()
					s.mu.Unlock()
				},
				Config: f.providerConfig(),
			}
			steps := []resource.TestStep{{Config: cfg}, destroy}
			if tc.imported {
				// Forget it, import it again, and destroy before any apply adopts it.
				steps = []resource.TestStep{
					{Config: cfg},
					{Config: forget(f, "mtncloud_mysql.db")},
					{Config: cfg, ResourceName: "mtncloud_mysql.db", ImportState: true, ImportStateId: "9801",
						ImportStatePersist: true},
					destroy,
				}
			}
			if tc.wantErr != "" {
				steps[len(steps)-1].ExpectError = regexp.MustCompile(tc.wantErr)
				steps = append(steps, resource.TestStep{Config: forget(f, "mtncloud_mysql.db")})
			}
			resource.UnitTest(t, resource.TestCase{ProtoV6ProviderFactories: factories(), Steps: steps})
			s.mu.Lock()
			defer s.mu.Unlock()
			if !slices.Equal(s.deleted, tc.want) {
				t.Errorf("deleted %v, want %v", s.deleted, tc.want)
			}
			insts.mu.Lock()
			defer insts.mu.Unlock()
			if _, kept := insts.insts[9701]; kept != (tc.wantErr != "") {
				t.Errorf("instance 9701 kept = %v", kept)
			}
		})
	}
}

// Names are checked against MTN Cloud at plan time, before anything is ordered.
func TestOrder_NamesCheckedAtPlan(t *testing.T) {
	f, s, _ := orderFakes(t)
	cfg := func(change, to string) string {
		body := `
  name             = "db-1"
  group            = "MTNNG_CLOUD_AZ_1"
  project          = "web-prod-Acme"
  plan             = "G2S4"
  root_volume_size = 40
  networks         = ["app-net"]
  version          = "8.4"
  floating_ip_pool = "public-network-02"
`
		return f.providerConfig() + "resource \"mtncloud_mysql\" \"db\" {" + strings.Replace(body, change, to, 1) + "}\n"
	}
	steps := []resource.TestStep{}
	for _, c := range []struct{ change, to, want string }{
		{`"8.4"`, `"5.7"`, `(?s)no version "5.7": choose one of 8.4, 8.0`},
		{`"app-net"`, `"nope"`, `no network named "nope"`},
		{`"G2S4"`, `"G9S9"`, `G9S9`},
		{`"web-prod-Acme"`, `"nope"`, `nope`},
		{`"public-network-02"`, `"public-network-09"`, `(?s)no floating IP pool "public-network-09"`},
	} {
		steps = append(steps, resource.TestStep{Config: cfg(c.change, c.to), PlanOnly: true, ExpectError: regexp.MustCompile(c.want)})
	}
	steps = append(steps, resource.TestStep{Config: cfg("", ""), PlanOnly: true, ExpectNonEmptyPlan: true})
	resource.UnitTest(t, resource.TestCase{ProtoV6ProviderFactories: factories(), Steps: steps})
	if s.orders != 0 {
		t.Errorf("%d orders placed by plans", s.orders)
	}
}

// Let's Encrypt runs against an instance: context "instance", the instance as the target.
// The floating IP pool is checked at plan against the router of the order's primary network: a
// router on another external network is an error, before anything is ordered.
func TestOrder_PoolCheckedAgainstRouter(t *testing.T) {
	f, s, _ := orderFakes(t)
	routers := newRouterStore()
	routers.routers[9990] = map[string]any{"id": 9990, "name": "edge", "status": "ok", "enabled": true,
		"externalNetwork": map[string]any{"id": 32, "name": "public-network-02"},
		"interfaces":      []any{map[string]any{"network": map[string]any{"id": 9350}}}}
	f.router = routers
	cfg := func(pool string) string {
		return f.providerConfig() + `resource "mtncloud_mysql" "db" {
  name             = "db-1"
  group            = "MTNNG_CLOUD_AZ_1"
  project          = "web-prod-Acme"
  plan             = "G2S4"
  root_volume_size = 40
  networks         = ["app-net"]
  version          = "8.4"
  floating_ip_pool = "` + pool + `"
}
`
	}
	resource.UnitTest(t, resource.TestCase{ProtoV6ProviderFactories: factories(), Steps: []resource.TestStep{
		{Config: cfg("public-network-01"), PlanOnly: true,
			ExpectError: regexp.MustCompile(`(?s)Floating IP pool doesn't match the router.*edge \(external network public-network-02\)`)},
		{Config: cfg("public-network-02"), PlanOnly: true, ExpectNonEmptyPlan: true},
	}})
	if s.orders != 0 {
		t.Errorf("%d orders placed by plans", s.orders)
	}
}

func TestLetsEncryptOrder(t *testing.T) {
	f, s, _ := orderFakes(t)
	cfg := f.providerConfig() + `
resource "mtncloud_letsencrypt_certificate" "web" {
  instance_id = "9705"
  domain_name = "shop.example.com"
  staging     = true
}
`
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories(),
		Steps: []resource.TestStep{
			{
				Config: cfg,
				Check: func(*terraform.State) error {
					s.mu.Lock()
					defer s.mu.Unlock()
					got, _ := json.Marshal(s.sent[9801])
					want := `{"config":{"sslDomainName":"shop.example.com","sslStaging":true},"context":"instance",` +
						`"targets":[{"id":"9705"}],"type":{"name":"MTN Let's Encrypt SSL"}}`
					if string(got) != want {
						t.Errorf("order item\n got %s\nwant %s", got, want)
					}
					return nil
				},
			},
			{Config: cfg, ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()}}},
			// A new target instance, or an input left out at order time, would never reach MTN Cloud.
			{Config: strings.Replace(cfg, `"9705"`, `"9706"`, 1), PlanOnly: true, ExpectError: regexp.MustCompile(`instance_id = "9705"`)},
			{Config: strings.Replace(cfg, "staging     = true", "staging     = true\n  admin_email = \"ops@example.com\"", 1), PlanOnly: true,
				ExpectError: regexp.MustCompile(`admin_email = nothing \(left out\)`)},
			{
				// Destroy deletes nothing: the certificate stays on the instance.
				Config: f.providerConfig(),
				Check: func(*terraform.State) error {
					s.mu.Lock()
					defer s.mu.Unlock()
					if len(s.deleted) != 0 {
						t.Errorf("deleted %v, want nothing", s.deleted)
					}
					return nil
				},
			},
		},
	})
}

// An imported order adopts its configuration on the next apply, and finds its instance by name.
func TestOrder_ImportAdopts(t *testing.T) {
	f, s, insts := orderFakes(t)
	s.items[9790] = map[string]any{"id": 9790, "type": map[string]any{"id": 38, "name": "MTN MySQL", "type": "workflow"},
		"status": "ORDERED", "execution": map[string]any{"id": 9791, "status": "success"}}
	insts.insts[9750] = map[string]any{"id": 9750, "name": "db-old", "status": "running", "config": map[string]any{"resourcePoolId": "pool-9201"}}
	cfg := f.providerConfig() + `
import {
  to = mtncloud_mysql.db
  id = "9790"
}
resource "mtncloud_mysql" "db" {
  name             = "db-old"
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
			{Config: cfg, ConfigStateChecks: []statecheck.StateCheck{
				statecheck.ExpectKnownValue("mtncloud_mysql.db", tfjsonpath.New("instance_ids"), knownvalue.ListExact([]knownvalue.Check{knownvalue.StringExact("9750")})),
			}},
			{Config: cfg, ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()}}},
			{Config: forget(f, "mtncloud_mysql.db")},
		},
	})
	if s.orders != 0 {
		t.Errorf("%d orders placed by an import", s.orders)
	}
}

// forget is a configuration that stops managing an order without destroying it (D-028).
func forget(f *fakeAPI, address string) string {
	return f.providerConfig() + `
removed {
  from = ` + address + `
  lifecycle {
    destroy = false
  }
}
`
}
