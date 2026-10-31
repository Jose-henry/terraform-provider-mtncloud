package legacy

import (
	"os"
	"strings"
	"testing"
)

// Resources and data sources deliberately stripped from upstream (CLAUDE.md §3.1).
// Re-adding one needs a decision record, and this list updated in the same change.
var (
	removedResources = []string{
		"aws_cloud", "aws_instance", "azure_cloud", "standard_cloud",
		"vsphere_cloud", "vsphere_instance", "vsphere_cloud_datastore_configuration",
		"vsphere_mks_cluster", "mvm_instance",
		"arm_app_blueprint", "arm_spec_template",
		"cloud_formation_app_blueprint", "cloud_formation_spec_template",
		// Master tenant only (D-024): customers can't use them.
		"appliance_setting", "backup_creation_policy", "backup_setting", "budget_policy",
		"cluster_package", "cluster_resource_name_policy", "cypher_access_policy",
		"delayed_delete_policy", "delete_approval_policy", "guidance_setting", "hostname_policy",
		"instance_name_policy", "license", "max_containers_policy", "max_cores_policy",
		"max_hosts_policy", "max_memory_policy", "max_storage_policy", "max_vms_policy",
		"monitoring_setting", "motd_policy", "network_quota_policy", "power_schedule_policy", "price",
		"price_set", "provision_approval_policy", "provisioning_setting", "resource_pool_group",
		"router_quota_policy", "service_plan", "tag_policy", "tenant", "tenant_role",
		"user_creation_policy", "user_group_creation_policy", "workflow_policy",
		// Not offered to customers in the Library, or task types their console lacks (owner audit, 2026-10-27).
		"app_blueprint_catalog_item", "boot_script", "cluster_layout", "file_template",
		"groovy_script_task", "helm_app_blueprint", "helm_spec_template", "instance_catalog_item",
		"instance_layout", "instance_type", "javascript_task", "kubernetes_app_blueprint",
		"kubernetes_spec_template", "node_type", "preseed_script", "python_script_task",
		"ruby_script_task", "script_template", "security_package", "terraform_app_blueprint",
		"terraform_spec_template", "workflow_catalog_item",
		// Integrations are set up in the console, not here (owner, 2026-10-28, D-028).
		"ansible_integration", "ansible_tower_integration", "chef_integration", "docker_registry_integration",
		"git_integration", "puppet_integration", "servicenow_integration", "vro_integration",
	}
	removedDataSources = []string{
		"cloud_folder",
		// Master tenant only (D-024). cloud_datastore was restored by D-010, then removed here.
		"cloud", "cloud_datastore", "clouds", "plan", "policies", "policy", "price", "price_set",
		"resource_pool", "tenant", "tenant_role", "tenants",
		// Owner audit, 2026-10-27. cypher_secret is replaced by mtncloud_cypher.
		"blueprint", "cloud_type", "cluster_type", "cypher_secret", "file_template", "instance_layout",
		"instance_type", "node_type", "provision_type", "script_template", "security_package",
		"spec_template",
	}
)

func TestProvider_InternalValidate(t *testing.T) {
	if err := Provider().InternalValidate(); err != nil {
		t.Fatalf("provider schema is invalid: %s", err)
	}
}

func TestProvider_TypeNamesUseMtncloudPrefix(t *testing.T) {
	p := Provider()
	for name := range p.ResourcesMap {
		if !strings.HasPrefix(name, "mtncloud_") {
			t.Errorf("resource %q does not start with mtncloud_", name)
		}
	}
	for name := range p.DataSourcesMap {
		if !strings.HasPrefix(name, "mtncloud_") {
			t.Errorf("data source %q does not start with mtncloud_", name)
		}
	}
}

func TestProvider_RemovedTypesStayRemoved(t *testing.T) {
	p := Provider()
	for _, name := range removedResources {
		if _, ok := p.ResourcesMap["mtncloud_"+name]; ok {
			t.Errorf("resource mtncloud_%s was removed from scope but is registered", name)
		}
	}
	for _, name := range removedDataSources {
		if _, ok := p.DataSourcesMap["mtncloud_"+name]; ok {
			t.Errorf("data source mtncloud_%s was removed from scope but is registered", name)
		}
	}
}

func TestProvider_NoUpstreamNameInSchema(t *testing.T) {
	p := Provider()
	check := func(kind, name, attr, desc string) {
		for _, s := range []string{attr, desc} {
			if strings.Contains(strings.ToLower(s), "morpheus") { // mtncloud:allow-upstream-name (asserts absence)
				t.Errorf("%s %s: attribute %q mentions the upstream product name: %q", kind, name, attr, s)
			}
		}
	}
	for attr, s := range p.Schema {
		check("provider", "config", attr, s.Description)
	}
	for name, r := range p.ResourcesMap {
		for attr, s := range r.Schema {
			check("resource", name, attr, s.Description)
		}
	}
	for name, r := range p.DataSourcesMap {
		for attr, s := range r.Schema {
			check("data source", name, attr, s.Description)
		}
	}
}

func TestProvider_ConfigReadsMtncloudEnvVars(t *testing.T) {
	p := Provider()
	want := map[string]string{
		"url":              "MTNCLOUD_API_URL",
		"access_token":     "MTNCLOUD_API_TOKEN",
		"tenant_subdomain": "MTNCLOUD_API_TENANT",
		"username":         "MTNCLOUD_API_USERNAME",
		"password":         "MTNCLOUD_API_PASSWORD",
		"secure":           "MTNCLOUD_API_SECURE",
	}
	for attr, env := range want {
		s, ok := p.Schema[attr]
		if !ok {
			t.Errorf("provider attribute %q is missing", attr)
			continue
		}
		sentinel := "from-env"
		if attr == "secure" {
			sentinel = "true"
		}
		t.Setenv(env, sentinel)
		got, err := s.DefaultFunc()
		if err != nil {
			t.Errorf("%s: DefaultFunc error: %s", attr, err)
			continue
		}
		if got == nil {
			t.Errorf("%s: not read from %s", attr, env)
		}
	}
}

func TestProvider_SecretsAreSensitive(t *testing.T) {
	p := Provider()
	for _, attr := range []string{"access_token", "password"} {
		if !p.Schema[attr].Sensitive {
			t.Errorf("provider attribute %q must be Sensitive", attr)
		}
	}
}

// The token file is read once here, and a file that can't be read fails configure with its reason,
// rather than sending no token.
func TestTokenOrFile(t *testing.T) {
	dir := t.TempDir()
	good, empty := dir+"/token", dir+"/empty"
	if err := os.WriteFile(good, []byte("abc123\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(empty, []byte(" \n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MTNCLOUD_API_TOKEN_FILE", "")
	for _, tc := range []struct {
		name, token, file, want, err string
	}{
		{"token wins", "tok", good, "tok", ""},
		{"file", "", good, "abc123", ""},
		{"neither", "", "", "", ""},
		{"missing file", "", dir + "/nope", "", "reading the access token file"},
		{"empty file", "", empty, "", "is empty"},
	} {
		got, err := tokenOrFile(tc.token, tc.file)
		if got != tc.want || (tc.err == "") != (err == nil) || (err != nil && !strings.Contains(err.Error(), tc.err)) {
			t.Errorf("%s: got %q, %v; want %q, error %q", tc.name, got, err, tc.want, tc.err)
		}
	}
}
