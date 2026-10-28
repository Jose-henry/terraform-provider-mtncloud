package legacy

import (
	"context"

	"github.com/hashicorp/terraform-plugin-sdk/v2/diag"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
)

func Provider() *schema.Provider {
	return &schema.Provider{
		Schema: map[string]*schema.Schema{
			// Optional (not Required) so the schema doesn't depend on MTNCLOUD_API_URL being set:
			// the Framework half of the mux must declare the same schema. Configure checks it.
			"url": {
				Type:        schema.TypeString,
				Optional:    true,
				Description: "The URL of the MTN Cloud console where requests will be directed.",
				DefaultFunc: schema.EnvDefaultFunc("MTNCLOUD_API_URL", nil),
			},

			"access_token": {
				Type:          schema.TypeString,
				Optional:      true,
				Sensitive:     true,
				Description:   "Access Token of MTN Cloud user. This can be used instead of authenticating with Username and Password.",
				DefaultFunc:   schema.EnvDefaultFunc("MTNCLOUD_API_TOKEN", nil),
				ConflictsWith: []string{"username", "password", "tenant_subdomain"},
			},

			"tenant_subdomain": {
				Type:          schema.TypeString,
				Optional:      true,
				Description:   "The tenant subdomain used for authentication",
				DefaultFunc:   schema.EnvDefaultFunc("MTNCLOUD_API_TENANT", nil),
				ConflictsWith: []string{"access_token"},
			},

			"username": {
				Type:          schema.TypeString,
				Optional:      true,
				Description:   "Username of MTN Cloud user for authentication",
				DefaultFunc:   schema.EnvDefaultFunc("MTNCLOUD_API_USERNAME", nil),
				ConflictsWith: []string{"access_token"},
			},

			"password": {
				Type:          schema.TypeString,
				Optional:      true,
				Sensitive:     true,
				Description:   "Password of MTN Cloud user for authentication",
				DefaultFunc:   schema.EnvDefaultFunc("MTNCLOUD_API_PASSWORD", nil),
				ConflictsWith: []string{"access_token"},
			},

			// Read by the Framework half only (internal/provider); here so the two schemas match.
			"group": {
				Type:        schema.TypeString,
				Optional:    true,
				Description: "Default group, by name, for resources that don't set one, and the group to use when your account has several. Can also be set with the MTNCLOUD_GROUP environment variable.",
			},
			"cloud": {
				Type:        schema.TypeString,
				Optional:    true,
				Description: "The cloud to use when your group has several, by name, for example MTNNG_CLOUD_AZ_1. Can also be set with the MTNCLOUD_CLOUD environment variable.",
			},
			"project": {
				Type:        schema.TypeString,
				Optional:    true,
				Description: "Default project, by name (for example web-prod), for resources that don't set one. Can also be set with the MTNCLOUD_PROJECT environment variable.",
			},

			// Verification is on unless the user turns it off (owner's decision, B-005).
			"secure": {
				Type:        schema.TypeBool,
				Optional:    true,
				Description: `Verify the MTN Cloud console's TLS certificate. Defaults to "true". Set to "false" only for a console with a self-signed certificate.`,
				DefaultFunc: schema.EnvDefaultFunc("MTNCLOUD_API_SECURE", true),
			},
		},

		ResourcesMap: map[string]*schema.Resource{
			"mtncloud_active_directory_identity_source": resourceActiveDirectoryIdentitySource(),
			"mtncloud_ansible_playbook_task":            resourceAnsiblePlaybookTask(),
			"mtncloud_ansible_tower_task":               resourceAnsibleTowerTask(),
			"mtncloud_api_option_list":                  resourceApiOptionList(),
			"mtncloud_checkbox_option_type":             resourceCheckboxOptionType(),
			"mtncloud_chef_bootstrap_task":              resourceChefBootstrapTask(),
			"mtncloud_contact":                          resourceContact(),
			"mtncloud_credential":                       resourceCredential(),
			"mtncloud_cypher_secret":                    resourceCypherSecret(),
			"mtncloud_cypher_tfvars":                    resourceCypherTFVars(),
			"mtncloud_email_task":                       resourceEmailTask(),
			"mtncloud_environment":                      resourceEnvironment(),
			"mtncloud_execute_schedule":                 resourceExecuteSchedule(),
			"mtncloud_form":                             resourceForm(),
			"mtncloud_group":                            resourceGroup(),
			"mtncloud_hidden_option_type":               resourceHiddenOptionType(),
			"mtncloud_ipv4_ip_pool":                     resourceIPv4IPPool(),
			"mtncloud_library_script_task":              resourceLibraryScriptTask(),
			"mtncloud_library_template_task":            resourceLibraryTemplateTask(),
			"mtncloud_key_pair":                         resourceKeyPair(),
			"mtncloud_manual_option_list":               resourceManualOptionList(),
			"mtncloud_nested_workflow_task":             resourceNestedWorkflowTask(),
			"mtncloud_network_domain":                   resourceNetworkDomain(),
			"mtncloud_number_option_type":               resourceNumberOptionType(),
			"mtncloud_operational_workflow":             resourceOperationalWorkflow(),
			"mtncloud_password_option_type":             resourcePasswordOptionType(),
			"mtncloud_powershell_script_task":           resourcePowerShellScriptTask(),
			"mtncloud_provisioning_workflow":            resourceProvisioningWorkflow(),
			"mtncloud_radio_list_option_type":           resourceRadioListOptionType(),
			"mtncloud_rest_option_list":                 resourceRestOptionList(),
			"mtncloud_restart_task":                     resourceRestartTask(),
			"mtncloud_saml_identity_source":             resourceSAMLIdentitySource(),
			"mtncloud_scale_threshold":                  resourceScaleThreshold(),
			"mtncloud_select_list_option_type":          resourceSelectListOptionType(),
			"mtncloud_shell_script_task":                resourceShellScriptTask(),
			"mtncloud_task_job":                         resourceTaskJob(),
			"mtncloud_text_option_type":                 resourceTextOptionType(),
			"mtncloud_textarea_option_type":             resourceTextAreaOptionType(),
			"mtncloud_typeahead_option_type":            resourceTypeAheadOptionType(),
			"mtncloud_user":                             resourceUser(),
			"mtncloud_user_group":                       resourceUserGroup(),
			"mtncloud_user_role":                        resourceUserRole(),
			"mtncloud_vro_task":                         resourceVrealizeOrchestratorTask(),
			"mtncloud_wiki_page":                        resourceWikiPage(),
			"mtncloud_workflow_job":                     resourceWorkflowJob(),
			"mtncloud_write_attributes_task":            resourceWriteAttributesTask(),
		},
		DataSourcesMap: map[string]*schema.Resource{
			"mtncloud_ansible_tower_job_template": dataSourceAnsibleTowerJobTemplate(),
			"mtncloud_ansible_tower_inventory":    dataSourceAnsibleTowerInventory(),
			"mtncloud_budget":                     dataSourceBudget(),
			"mtncloud_catalog_item_type":          dataSourceCatalogItemType(),
			"mtncloud_chef_server":                dataSourceChefServer(),
			"mtncloud_contact":                    dataSourceContact(),
			"mtncloud_credential":                 dataSourceCredential(),
			"mtncloud_domain":                     dataSourceDomain(),
			"mtncloud_environment":                dataSourceEnvironment(),
			"mtncloud_environments":               dataSourceEnvironments(),
			"mtncloud_execute_schedule":           dataSourceExecuteSchedule(),
			"mtncloud_git_integration":            dataSourceGitIntegration(),
			"mtncloud_group":                      dataSourceGroup(),
			"mtncloud_groups":                     dataSourceGroups(),
			"mtncloud_integration":                dataSourceIntegration(),
			"mtncloud_job":                        dataSourceJob(),
			"mtncloud_key_pair":                   dataSourceKeyPair(),
			"mtncloud_network_group":              dataSourceNetworkGroup(),
			"mtncloud_network_subnet":             dataSourceNetworkSubnet(),
			"mtncloud_option_list":                dataSourceOptionList(),
			"mtncloud_option_type":                dataSourceOptionType(),
			"mtncloud_permission_set":             dataSourcePermissionSet(),
			"mtncloud_power_schedule":             dataSourcePowerSchedule(),
			"mtncloud_servicenow_workflow":        dataSourceServiceNowWorkflow(),
			"mtncloud_storage_bucket":             dataSourceStorageBucket(),
			"mtncloud_storage_volume_type":        dataSourceStorageVolumeType(),
			"mtncloud_storage_volume":             dataSourceStorageVolume(),
			"mtncloud_task":                       dataSourceTask(),
			"mtncloud_tasks":                      dataSourceTasks(),
			"mtncloud_user_group":                 dataSourceUserGroup(),
			"mtncloud_user_groups":                dataSourceUserGroups(),
			"mtncloud_user_role":                  dataSourceUserRole(),
			"mtncloud_vdi_pool":                   dataSourceVDIPool(),
			"mtncloud_virtual_image":              dataSourceVirtualImage(),
			"mtncloud_virtual_images":             dataSourceVirtualImages(),
			"mtncloud_vro_workflow":               dataSourceVrealizeOrchestratorWorkflow(),
			"mtncloud_workflow":                   dataSourceWorkflow(),
		},
		ConfigureContextFunc: providerConfigure,
	}
}

func providerConfigure(ctx context.Context, d *schema.ResourceData) (interface{}, diag.Diagnostics) {
	// An unknown url (from a resource not created yet) reads as "" during plan: that isn't "not set".
	if d.Get("url").(string) == "" && urlKnown(d) {
		return nil, diag.Errorf("the MTN Cloud console URL is not set: set url in the provider block, or MTNCLOUD_API_URL")
	}
	config := Config{
		Url:             d.Get("url").(string),
		AccessToken:     d.Get("access_token").(string),
		TenantSubdomain: d.Get("tenant_subdomain").(string),
		Username:        d.Get("username").(string),
		Password:        d.Get("password").(string),
		Insecure:        !d.Get("secure").(bool), //.(bool),
	}

	return config.Client()
}

func urlKnown(d *schema.ResourceData) bool {
	raw := d.GetRawConfig()
	if raw.IsNull() || !raw.IsKnown() || !raw.Type().IsObjectType() {
		return raw.IsKnown()
	}
	return raw.GetAttr("url").IsKnown()
}
