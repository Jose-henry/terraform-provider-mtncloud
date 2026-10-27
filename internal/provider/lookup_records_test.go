package provider

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/knownvalue"
	"github.com/hashicorp/terraform-plugin-testing/statecheck"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"
)

// recordFixtures maps each list endpoint of the table-driven lookups to its synthetic fixture
// (testdata/lookups/records): three objects, IDs 101-103, named alpha, beta and beta.
var recordFixtures = map[string]string{
	"/api/apps": "apps", "/api/certificates": "certificates", "/api/load-balancers": "loadBalancers",
	"/api/clusters": "clusters", "/api/catalog/items": "items", "/api/users": "users",
	"/api/user-sources": "userSources", "/api/roles": "roles", "/api/subnets": "subnets",
	"/api/storage-buckets": "storageBuckets", "/api/storage-volumes": "storageVolumes",
	"/api/data-stores": "datastores", "/api/networks/domains": "networkDomains", "/api/key-pairs": "keyPairs",
	"/api/credentials": "credentials", "/api/task-sets": "taskSets", "/api/jobs": "jobs",
	"/api/integrations": "integrations", "/api/library/option-type-forms": "optionTypeForms", "/api/cypher": "cyphers",
}

func newRecordsAPI(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		file := ""
		if r.URL.Path == "/api/whoami" {
			file = "whoami_subtenant.json"
		} else if f, ok := recordFixtures[r.URL.Path]; ok {
			file = filepath.Join("records", f+".json")
		}
		if file == "" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		body, err := os.ReadFile(filepath.Join("..", "client", "testdata", "lookups", file))
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// TestRecordLookups covers every table-driven lookup: the list, a name filter, a lookup by name
// and by ID, field mapping (nested, list and object-or-string fields), and the errors.
func TestRecordLookups(t *testing.T) {
	cfg := `
provider "mtncloud" {
  url          = "` + newRecordsAPI(t) + `"
  access_token = "test-token"
}
`
	// type name of the list (and its list attribute) -> a text field checked on the first item.
	many := map[string]string{
		"apps": "group", "ssl_certificates": "domain_name", "load_balancers": "cloud", "clusters": "cloud",
		"catalog_orders": "catalog_item", "users": "email", "identity_sources": "default_role", "user_roles": "role_type",
		"network_subnets": "network", "storage_buckets": "bucket_name", "storage_volumes": "datastore", "domains": "fqdn",
		"key_pairs": "fingerprint", "credentials": "type", "workflows": "type", "jobs": "type", "integrations": "type",
		"forms": "code", "cyphers": "created_by",
	}
	// type name of the single lookup -> its name attribute.
	one := map[string]string{
		"app": "name", "ssl_certificate": "name", "load_balancer": "name", "cluster": "name", "catalog_order": "name",
		"user": "username", "identity_source": "name", "datastore": "name", "form": "name",
	}
	nameOf := map[string]string{"users": "username", "user_roles": "name", "cyphers": "key"}

	var b strings.Builder
	var checks []statecheck.StateCheck
	for ds, f := range many {
		fmt.Fprintf(&b, "data \"mtncloud_%s\" \"all\" {}\n", ds)
		fmt.Fprintf(&b, "data \"mtncloud_%s\" \"a\" {\n  name_regex = \"^al\"\n}\n", ds)
		all := tfjsonpath.New(ds)
		n := nameOf[ds]
		if n == "" {
			n = "name"
		}
		checks = append(checks,
			statecheck.ExpectKnownValue("data.mtncloud_"+ds+".all", all, knownvalue.ListSizeExact(3)),
			// Sorted by name, then ID: beta 102 before beta 103.
			statecheck.ExpectKnownValue("data.mtncloud_"+ds+".all", all.AtSliceIndex(1).AtMapKey("id"), knownvalue.Int64Exact(102)),
			statecheck.ExpectKnownValue("data.mtncloud_"+ds+".a", all, knownvalue.ListSizeExact(1)),
			statecheck.ExpectKnownValue("data.mtncloud_"+ds+".a", all.AtSliceIndex(0).AtMapKey(n), knownvalue.StringExact("alpha")),
			statecheck.ExpectKnownValue("data.mtncloud_"+ds+".a", all.AtSliceIndex(0).AtMapKey(f), knownvalue.StringExact(f+"-101")),
		)
	}
	for ds, n := range one {
		fmt.Fprintf(&b, "data \"mtncloud_%s\" \"by_name\" {\n  %s = \"alpha\"\n}\n", ds, n)
		fmt.Fprintf(&b, "data \"mtncloud_%s\" \"by_id\" {\n  id = 103\n}\n", ds)
		checks = append(checks,
			statecheck.ExpectKnownValue("data.mtncloud_"+ds+".by_name", tfjsonpath.New("id"), knownvalue.Int64Exact(101)),
			statecheck.ExpectKnownValue("data.mtncloud_"+ds+".by_id", tfjsonpath.New(n), knownvalue.StringExact("beta")),
		)
	}
	at := func(ds, attr string) tfjsonpath.Path { return tfjsonpath.New(ds).AtSliceIndex(0).AtMapKey(attr) }
	checks = append(checks,
		// A list read through a list of objects, a list of numbers, numbers and flags.
		statecheck.ExpectKnownValue("data.mtncloud_users.a", at("users", "roles"),
			knownvalue.ListExact([]knownvalue.Check{knownvalue.StringExact("roles-a"), knownvalue.StringExact("roles-b")})),
		statecheck.ExpectKnownValue("data.mtncloud_apps.a", at("apps", "instances"),
			knownvalue.ListExact([]knownvalue.Check{knownvalue.Int64Exact(102), knownvalue.Int64Exact(103)})),
		statecheck.ExpectKnownValue("data.mtncloud_apps.a", at("apps", "instance_count"), knownvalue.Int64Exact(1010)),
		statecheck.ExpectKnownValue("data.mtncloud_network_subnets.a", at("network_subnets", "dhcp"), knownvalue.Bool(true)),
		statecheck.ExpectKnownValue("data.mtncloud_network_subnets.a", at("network_subnets", "network_id"), knownvalue.Int64Exact(1010)),
		// The job's type is {id, name} in its fixture: the name is shown.
		statecheck.ExpectKnownValue("data.mtncloud_jobs.a", at("jobs", "type"), knownvalue.StringExact("type-101")),
		statecheck.ExpectKnownValue("data.mtncloud_cyphers.a", at("cyphers", "lease_timeout"), knownvalue.Int64Exact(1010)),
	)

	// Filters: text (case ignored), flags, and lists where every value given must be there.
	b.WriteString(`
data "mtncloud_apps" "status" {
  status = "STATUS-102"
}
data "mtncloud_users" "enabled" {
  enabled = true
}
data "mtncloud_users" "role" {
  roles = ["ROLES-A", "roles-b"]
}
data "mtncloud_users" "no_role" {
  roles = ["roles-a", "nope"]
}
data "mtncloud_workflows" "labels_and_name" {
  labels     = ["labels-a"]
  name_regex = "^b"
}
`)
	checks = append(checks,
		statecheck.ExpectKnownValue("data.mtncloud_apps.status", tfjsonpath.New("apps"), knownvalue.ListSizeExact(1)),
		statecheck.ExpectKnownValue("data.mtncloud_apps.status", tfjsonpath.New("apps").AtSliceIndex(0).AtMapKey("id"), knownvalue.Int64Exact(102)),
		statecheck.ExpectKnownValue("data.mtncloud_users.enabled", tfjsonpath.New("users"), knownvalue.ListSizeExact(3)),
		statecheck.ExpectKnownValue("data.mtncloud_users.role", tfjsonpath.New("users"), knownvalue.ListSizeExact(3)),
		statecheck.ExpectKnownValue("data.mtncloud_users.no_role", tfjsonpath.New("users"), knownvalue.ListSizeExact(0)),
		statecheck.ExpectKnownValue("data.mtncloud_workflows.labels_and_name", tfjsonpath.New("workflows"), knownvalue.ListSizeExact(2)),
	)
	steps := []resource.TestStep{{Config: cfg + b.String(), ConfigStateChecks: checks}}
	for ds, n := range one {
		lookup := func(attrs string) string {
			return cfg + "data \"mtncloud_" + ds + "\" \"x\" {\n" + attrs + "\n}\n"
		}
		steps = append(steps,
			resource.TestStep{Config: lookup("  " + n + ` = "beta"`), ExpectError: regexp.MustCompile(`(?s)Several .* match.*IDs\s+102,\s+103`)},
			resource.TestStep{Config: lookup("  " + n + ` = "nope"`), ExpectError: regexp.MustCompile(`(?s)No such .*Yours: alpha,\s+beta\.`)},
			resource.TestStep{Config: lookup("  id = 999"), ExpectError: regexp.MustCompile(`(?s)No such .*999`)},
			resource.TestStep{Config: lookup(""), ExpectError: regexp.MustCompile(`Exactly one of these attributes must be configured`)},
		)
	}
	// The test ends on a valid configuration: the destroy that follows re-reads the last one.
	steps = append(steps, resource.TestStep{Config: cfg})
	resource.UnitTest(t, resource.TestCase{ProtoV6ProviderFactories: factories(), Steps: steps})
}
