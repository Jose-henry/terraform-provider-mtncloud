package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/knownvalue"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/statecheck"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"

	"github.com/hashicorp/terraform-plugin-framework/types"
)

// fakeAPI serves the scrubbed fixtures the client contract tests use. Requests under
// /api/security-groups go to sg, a stateful fake (security_group_test.go), when it is set.
type fakeAPI struct {
	srv    *httptest.Server
	mu     sync.Mutex
	calls  map[string]int
	sg     http.Handler
	net    http.Handler // /api/networks, a stateful fake (network_test.go), when set
	router http.Handler // /api/networks/routers, a stateful fake (router_test.go), when set
}

func newFakeAPI(t *testing.T) *fakeAPI {
	t.Helper()
	routes := map[string]string{
		"/api/whoami":                            "whoami_subtenant.json",
		"/api/options/groups":                    "options_groups.json",
		"/api/options/clouds":                    "options_clouds.json",
		"/api/options/zonePools":                 "options_zone_pools.json",
		"/api/options/zoneSecurityGroup":         "options_security_groups.json",
		"/api/options/openstackExternalNetworks": "options_external_networks.json",
		"/api/options/datastores":                "options_datastores.json",
		"/api/options/creatableNetworkTypes":     "options_network_types.json",
		"/api/options/networkServer":             "options_network_servers.json",
		"/api/instances/service-plans":           "service_plans.json",
		"/api/catalog/types":                     "catalog_types.json",
		"/api/instance-types":                    "instance_types.json",
		"/api/network-router-types":              "router_types.json",
	}
	f := &fakeAPI{calls: map[string]int{}}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.calls[r.URL.Path]++
		sg, net, router := f.sg, f.net, f.router
		f.mu.Unlock()
		if router != nil && strings.HasPrefix(r.URL.Path, "/api/networks/routers") && r.Header.Get("Authorization") == "Bearer test-token" {
			router.ServeHTTP(w, r)
			return
		}
		if net != nil && (r.URL.Path == "/api/networks" || strings.HasPrefix(r.URL.Path, "/api/networks/")) && r.Header.Get("Authorization") == "Bearer test-token" {
			net.ServeHTTP(w, r)
			return
		}
		if sg != nil && strings.HasPrefix(r.URL.Path, "/api/security-groups") && r.Header.Get("Authorization") == "Bearer test-token" {
			sg.ServeHTTP(w, r)
			return
		}
		file, ok := routes[r.URL.Path]
		if !ok || r.Header.Get("Authorization") != "Bearer test-token" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if off := r.URL.Query().Get("offset"); off != "" && off != "0" {
			_, _ = w.Write([]byte(`{"instanceTypes":[],"catalogItemTypes":[]}`))
			return
		}
		body, err := os.ReadFile(filepath.Join("..", "client", "testdata", "lookups", file))
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		_, _ = w.Write(body)
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeAPI) providerConfig() string {
	return `
provider "mtncloud" {
  url          = "` + f.srv.URL + `"
  access_token = "test-token"
}
`
}

func factories() map[string]func() (tfprotov6.ProviderServer, error) {
	return map[string]func() (tfprotov6.ProviderServer, error){
		"mtncloud": func() (tfprotov6.ProviderServer, error) {
			s, err := Server(context.Background(), "test")
			if err != nil {
				return nil, err
			}
			return s(), nil
		},
	}
}

// The mux refuses to serve if the legacy and Framework provider schemas differ.
func TestMux_ProviderSchemasMatch(t *testing.T) {
	s, err := Server(t.Context(), "test")
	if err != nil {
		t.Fatal(err)
	}
	resp, err := s().GetProviderSchema(t.Context(), &tfprotov6.GetProviderSchemaRequest{})
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range resp.Diagnostics {
		if d.Severity == tfprotov6.DiagnosticSeverityError {
			t.Errorf("%s: %s", d.Summary, d.Detail)
		}
	}
	for _, name := range []string{"mtncloud_projects", "mtncloud_images", "mtncloud_plans", "mtncloud_datastores", "mtncloud_security_groups", "mtncloud_external_networks", "mtncloud_catalog_items", "mtncloud_cloud_datastore"} {
		if _, ok := resp.DataSourceSchemas[name]; !ok {
			t.Errorf("data source %s is not served", name)
		}
	}
	if _, ok := resp.ResourceSchemas["mtncloud_tenant"]; !ok {
		t.Error("legacy resources are not served through the mux")
	}
}

func TestDataSources_ReadAndStayStable(t *testing.T) {
	f := newFakeAPI(t)
	config := f.providerConfig() + `
data "mtncloud_projects" "all" {}
data "mtncloud_projects" "web" {
  name_regex = "^web"
}
data "mtncloud_images" "ubuntu" {
  name_regex = "Ubuntu"
}
data "mtncloud_images" "all" {}
data "mtncloud_plans" "big" {
  project       = "web-prod"
  image         = "MTN-U24.04LTS"
  min_memory_gb = 4
}
data "mtncloud_datastores" "web" {
  project = "web-prod-Acme"
}
data "mtncloud_security_groups" "web" {
  project = "web-prod"
}
data "mtncloud_external_networks" "all" {}
data "mtncloud_catalog_items" "featured" {
  name_regex = "Instance"
}
`
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories(),
		Steps: []resource.TestStep{
			{
				Config: config,
				ConfigStateChecks: []statecheck.StateCheck{
					// Short names, sorted by full name; the tenant suffix is stripped.
					statecheck.ExpectKnownValue("data.mtncloud_projects.all", tfjsonpath.New("projects"), knownvalue.ListExact([]knownvalue.Check{
						knownvalue.ObjectPartial(map[string]knownvalue.Check{"name": knownvalue.StringExact("admin")}),
						knownvalue.ObjectPartial(map[string]knownvalue.Check{"name": knownvalue.StringExact("data"), "full_name": knownvalue.StringExact("data-Acme")}),
						knownvalue.ObjectPartial(map[string]knownvalue.Check{"name": knownvalue.StringExact("web-prod"), "id": knownvalue.Int64Exact(9201)}),
					})),
					statecheck.ExpectKnownValue("data.mtncloud_projects.web", tfjsonpath.New("projects"), knownvalue.ListSizeExact(1)),
					// Images: OS only (the MK8S app type is left out), filtered by name.
					statecheck.ExpectKnownValue("data.mtncloud_images.all", tfjsonpath.New("images"), knownvalue.ListSizeExact(2)),
					statecheck.ExpectKnownValue("data.mtncloud_images.ubuntu", tfjsonpath.New("images"), knownvalue.ListExact([]knownvalue.Check{
						knownvalue.ObjectExact(map[string]knownvalue.Check{
							"id": knownvalue.Int64Exact(89), "code": knownvalue.StringExact("MTN-U24.04LTS"),
							"name": knownvalue.StringExact("MTN Ubuntu Server 24.04.3LTS"), "layout_id": knownvalue.Int64Exact(309),
						}),
					})),
					// Plans: filtered by memory, sizes in GB.
					statecheck.ExpectKnownValue("data.mtncloud_plans.big", tfjsonpath.New("plans"), knownvalue.ListExact([]knownvalue.Check{
						knownvalue.ObjectPartial(map[string]knownvalue.Check{
							"name": knownvalue.StringExact("G2S4"), "cores": knownvalue.Int64Exact(2),
							"memory_gb": knownvalue.Float64Exact(4), "root_disk_gb": knownvalue.Int64Exact(10),
						}),
					})),
					statecheck.ExpectKnownValue("data.mtncloud_datastores.web", tfjsonpath.New("datastores"), knownvalue.ListExact([]knownvalue.Check{
						knownvalue.ObjectExact(map[string]knownvalue.Check{"id": knownvalue.Int64Exact(414), "name": knownvalue.StringExact("ENCR_AT_REST")}),
						knownvalue.ObjectExact(map[string]knownvalue.Check{"id": knownvalue.Int64Exact(2), "name": knownvalue.StringExact("__DEFAULT__")}),
					})),
					statecheck.ExpectKnownValue("data.mtncloud_security_groups.web", tfjsonpath.New("security_groups").AtSliceIndex(0), knownvalue.ObjectExact(map[string]knownvalue.Check{
						"id": knownvalue.Int64Exact(9301), "name": knownvalue.StringExact("default"),
						"external_id": knownvalue.StringExact("11111111-1111-4111-8111-111111111111"),
					})),
					statecheck.ExpectKnownValue("data.mtncloud_external_networks.all", tfjsonpath.New("external_networks"), knownvalue.ListSizeExact(2)),
					statecheck.ExpectKnownValue("data.mtncloud_catalog_items.featured", tfjsonpath.New("items"), knownvalue.ListExact([]knownvalue.Check{
						knownvalue.ObjectPartial(map[string]knownvalue.Check{"name": knownvalue.StringExact("Create Instance"), "featured": knownvalue.Bool(true)}),
					})),
				},
			},
			{
				// Same config again: nothing to change.
				Config:           config,
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()}},
			},
		},
	})
	// Terraform starts a new provider process per command, and each process has its own cache.
	// Within a process, whoami and the group lookup run once however many data sources need them,
	// so the two counts match: one each per process.
	f.mu.Lock()
	defer f.mu.Unlock()
	if who, groups := f.calls["/api/whoami"], f.calls["/api/options/groups"]; who != groups || who == 0 {
		t.Errorf("whoami called %d times, groups %d: want one of each per provider process", who, groups)
	}
}

func TestDataSources_ClearErrors(t *testing.T) {
	f := newFakeAPI(t)
	for name, tc := range map[string]struct{ config, want string }{
		"unknown project lists the real ones": {
			`data "mtncloud_security_groups" "x" { project = "nope" }`,
			`no project named "nope". Your projects: admin, data, web-prod`,
		},
		"unknown image lists OS images": {
			`data "mtncloud_plans" "x" {
  project = "web-prod"
  image   = "MTN-U99"
}`,
			`no image with code "MTN-U99". Available images: MTN-U24.04LTS, MTN-WINSVR2025`,
		},
		"bad regex fails at plan time": {
			`data "mtncloud_images" "x" { name_regex = "(" }`,
			`Invalid regular expression`,
		},
		"bad catalog type fails at plan time": {
			`data "mtncloud_catalog_items" "x" { type = "app" }`,
			`value must be one of`,
		},
	} {
		t.Run(name, func(t *testing.T) {
			resource.UnitTest(t, resource.TestCase{
				ProtoV6ProviderFactories: factories(),
				Steps: []resource.TestStep{{
					Config:      f.providerConfig() + tc.config,
					ExpectError: regexp.MustCompile(regexp.QuoteMeta(tc.want)),
				}},
			})
		})
	}
}

// A provider block that depends on values known only after apply must not crash the plugin:
// the data source fails with an explanation (review finding: nil client panic).
func TestUnknownProviderConfig_IsAnErrorNotAPanic(t *testing.T) {
	for name, attr := range map[string]string{"url": "url", "secure": "secure"} {
		t.Run(name, func(t *testing.T) {
			value := `terraform_data.later.output`
			config := `
resource "terraform_data" "later" {
  input = ` + map[string]string{"url": `"https://console.example.com"`, "secure": "true"}[name] + `
}

provider "mtncloud" {
  url          = "https://console.example.com"
  access_token = "test-token"
  ` + attr + ` = ` + value + `
}

data "mtncloud_projects" "all" {}
`
			if name == "url" {
				config = strings.Replace(config, "  url          = \"https://console.example.com\"\n", "", 1)
			}
			resource.UnitTest(t, resource.TestCase{
				ProtoV6ProviderFactories: factories(),
				ExternalProviders:        map[string]resource.ExternalProvider{},
				Steps: []resource.TestStep{{
					Config:      config,
					ExpectError: regexp.MustCompile(`MTN Cloud provider is not configured yet`),
				}},
			})
		})
	}
}

func TestResolveAuth_MatchesLegacyPrecedence(t *testing.T) {
	t.Setenv("MTNCLOUD_API_TOKEN", "stale-token") // ggignore: fake test credential
	t.Setenv("MTNCLOUD_API_USERNAME", "")
	t.Setenv("MTNCLOUD_API_PASSWORD", "")
	t.Setenv("MTNCLOUD_API_TENANT", "")

	// Username and password in the block win over a token from the environment.
	got := resolveAuth(providerModel{Username: types.StringValue("jdoe"), Password: types.StringValue("pw"), TenantSubdomain: types.StringValue("acme")}) // ggignore: fake test credential
	if got != (credentials{username: "jdoe", password: "pw", tenant: "acme"}) {                                                                           // ggignore: fake test credential
		t.Errorf("block credentials: got %+v", got)
	}
	// No username anywhere: the token is used.
	if got := resolveAuth(providerModel{}); got != (credentials{token: "stale-token"}) { // ggignore: fake test credential
		t.Errorf("token: got %+v", got)
	}
	// A username from the environment also means password login, as in legacy.
	t.Setenv("MTNCLOUD_API_USERNAME", "envuser")
	t.Setenv("MTNCLOUD_API_PASSWORD", "envpw")                                                             // ggignore: fake test credential
	if got := resolveAuth(providerModel{}); got != (credentials{username: "envuser", password: "envpw"}) { // ggignore: fake test credential
		t.Errorf("env username: got %+v", got)
	}
}
