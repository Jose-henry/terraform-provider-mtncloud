"""Generate templates/guides/features.md.tmpl: the user-facing list of everything the provider supports.

Usage: go run ./tools/listtypes | python3 scripts/gen-features.py
(run by `make docs`, before tfplugindocs renders the template into docs/guides/features.md)

The category map below is the only hand-written part. The script fails if a registered type is in
no category, or a category names a type that isn't registered, so the guide can't drift from the
provider. Planned (not yet built) types live in PLANNED and are shown separately.
"""
import sys
from pathlib import Path

OUT = Path(__file__).resolve().parent.parent / "templates" / "guides" / "features.md.tmpl"

# (title, intro, [type names without the mtncloud_ prefix])
RESOURCES = [
    ("Tenancy, users and access",
     "Tenants, users, roles, groups and identity sources.",
     ["tenant", "tenant_role", "user", "user_role", "user_group", "active_directory_identity_source",
      "saml_identity_source", "contact", "credential", "group", "environment", "resource_pool_group"]),
    ("Appliance configuration (master tenant only)",
     "Platform-wide settings. Only the MTN Cloud master tenant can manage these.",
     ["appliance_setting", "backup_setting", "guidance_setting", "monitoring_setting",
      "provisioning_setting", "license"]),
    ("Library: instance types, layouts and templates",
     "What can be provisioned and how: instance types, layouts, node types, cluster layouts, "
     "templates and scripts.",
     ["instance_type", "instance_layout", "node_type", "cluster_layout", "cluster_package",
      "file_template", "script_template", "boot_script", "preseed_script", "security_package",
      "wiki_page"]),
    ("Blueprints and spec templates",
     "Kubernetes, Helm and Terraform blueprints and spec templates.",
     ["helm_app_blueprint", "kubernetes_app_blueprint", "terraform_app_blueprint",
      "helm_spec_template", "kubernetes_spec_template", "terraform_spec_template"]),
    ("Service catalog and forms",
     "Self-service catalog items and the forms customers fill in to order them.",
     ["app_blueprint_catalog_item", "instance_catalog_item", "workflow_catalog_item", "form"]),
    ("Inputs: option lists and option types",
     "The inputs used by catalog items, forms, workflows and layouts.",
     ["api_option_list", "manual_option_list", "rest_option_list", "checkbox_option_type",
      "hidden_option_type", "number_option_type", "password_option_type", "radio_list_option_type",
      "select_list_option_type", "text_option_type", "textarea_option_type",
      "typeahead_option_type"]),
    ("Automation: tasks",
     "Building blocks for workflows.",
     ["ansible_playbook_task", "ansible_tower_task", "chef_bootstrap_task", "email_task",
      "groovy_script_task", "javascript_task", "library_script_task", "library_template_task",
      "nested_workflow_task", "powershell_script_task", "python_script_task", "restart_task",
      "ruby_script_task", "shell_script_task", "vro_task", "write_attributes_task"]),
    ("Automation: workflows, jobs and schedules",
     "Workflows that chain tasks, and jobs that run them on a schedule.",
     ["operational_workflow", "provisioning_workflow", "task_job", "workflow_job",
      "execute_schedule"]),
    ("Integrations",
     "Connections to configuration management, source control, registries and ITSM tools.",
     ["ansible_integration", "ansible_tower_integration", "chef_integration",
      "docker_registry_integration", "git_integration", "puppet_integration",
      "servicenow_integration", "vro_integration"]),
    ("Policies and governance",
     "Quotas, approvals, naming, budgets and other guard rails.",
     ["backup_creation_policy", "budget_policy", "cluster_resource_name_policy",
      "cypher_access_policy", "delayed_delete_policy", "delete_approval_policy", "hostname_policy",
      "instance_name_policy", "max_containers_policy", "max_cores_policy", "max_hosts_policy",
      "max_memory_policy", "max_storage_policy", "max_vms_policy", "motd_policy",
      "network_quota_policy", "power_schedule_policy", "provision_approval_policy",
      "router_quota_policy", "tag_policy", "user_creation_policy", "user_group_creation_policy",
      "workflow_policy"]),
    ("Plans and pricing",
     "Service plans and the prices attached to them.",
     ["service_plan", "price", "price_set"]),
    ("Networking",
     "IP pools and DNS domains. MTN Network and MTN Router resources are planned (see below).",
     ["ipv4_ip_pool", "network_domain"]),
    ("Secrets and keys",
     "Cypher secrets and SSH key pairs.",
     ["cypher_secret", "cypher_tfvars", "key_pair"]),
    ("Scaling",
     "Autoscaling thresholds.",
     ["scale_threshold"]),
]

DATA_SOURCES = [
    ("Pick by name: what can I deploy?",
     ["projects", "images", "plans", "datastores", "security_groups", "external_networks",
      "catalog_items"]),
    ("Tenancy, users and access",
     ["tenant", "tenants", "tenant_role", "user_role", "user_group", "user_groups",
      "permission_set", "contact", "credential", "group", "groups", "environment", "environments"]),
    ("Cloud, projects and capacity",
     ["cloud", "clouds", "cloud_type", "cloud_datastore", "resource_pool", "provision_type"]),
    ("Networking",
     ["network", "networks", "network_group", "network_subnet", "domain"]),
    ("Images, plans and library",
     ["instance_type", "instance_layout", "node_type", "cluster_type", "plan", "virtual_image",
      "virtual_images", "spec_template", "file_template", "script_template", "security_package",
      "blueprint"]),
    ("Storage",
     ["storage_bucket", "storage_volume", "storage_volume_type"]),
    ("Catalog and inputs",
     ["catalog_item_type", "option_list", "option_type", "vdi_pool"]),
    ("Automation and integrations",
     ["task", "tasks", "workflow", "job", "execute_schedule", "power_schedule", "integration",
      "git_integration", "ansible_tower_inventory", "ansible_tower_job_template", "chef_server",
      "servicenow_workflow", "vro_workflow"]),
    ("Policies, plans and pricing",
     ["policy", "policies", "budget", "price", "price_set"]),
    ("Secrets and keys",
     ["cypher_secret", "key_pair"]),
]

# New MTN-specific types: designed, not yet built. Kept here so the guide shows the roadmap.
PLANNED = [
    ("Resources", [
        ("mtncloud_project", "Order a project (resource pool) by name and wait until it and its default security group have synced."),
        ("mtncloud_instance", "OpenStack VM from an MTN OS image (applications come from the catalog): plan by size name (for example `G2S4`), project, networks, several security groups, volumes on any Cinder datastore, optional floating IP."),
        ("mtncloud_security_group", "Security group with rules, placed in a project."),
        ("mtncloud_security_group_rule", "A single rule, for rules managed from another module."),
        ("mtncloud_network", "MTN Network: private network and subnet in a project (CIDR, gateway, MTN DNS, DHCP)."),
        ("mtncloud_router", "MTN Router: connects MTN Networks to each other and, optionally, to the internet through a public network."),
        ("mtncloud_floating_ip", "A public IP that can outlive an instance."),
        ("mtncloud_catalog_order", "Order any catalog service (Kubernetes, managed databases, VPN, firewall, NAT, BYOL appliances and more)."),
        ("mtncloud_object_storage", "MOS (MTN Object Storage): S3-compatible keys plus optional buckets, in one resource."),
        ("mtncloud_app", "Multi-instance apps."),
        ("mtncloud_ssl_certificate", "Certificates."),
    ]),
    ("Data sources", [
        ("mtncloud_routers", "MTN Routers in a project."),
    ]),
]


def main():
    registered = {"resource": set(), "data": set()}
    for line in sys.stdin:
        kind, name = line.split()
        registered[kind].add(name.removeprefix("mtncloud_"))

    errors = []
    for kind, groups in (("resource", [g[2] for g in RESOURCES]), ("data", [g[1] for g in DATA_SOURCES])):
        listed = [n for g in groups for n in g]
        dupes = {n for n in listed if listed.count(n) > 1}
        if dupes:
            errors.append(f"{kind}: listed twice: {sorted(dupes)}")
        missing = registered[kind] - set(listed)
        unknown = set(listed) - registered[kind]
        if missing:
            errors.append(f"{kind}: registered but in no category (add it to scripts/gen-features.py): {sorted(missing)}")
        if unknown:
            errors.append(f"{kind}: in a category but not registered: {sorted(unknown)}")
    if not registered["resource"] or not registered["data"]:
        errors.append("no input: pipe `go run ./tools/listtypes` into this script")
    if errors:
        sys.exit("gen-features: " + "\n".join(errors))

    nr, nd = len(registered["resource"]), len(registered["data"])
    out = [
        "---",
        'page_title: "Features"',
        "description: |-",
        "  Everything the MTN Cloud provider can manage today, and what is coming next.",
        "---",
        "",
        "# Features",
        "",
        "<!-- Generated by scripts/gen-features.py from the provider's registered types. Do not edit. -->",
        "",
        f"The provider manages **{nr} resource types** and reads **{nd} data source types** today. "
        "It targets MTN Cloud on OpenStack (`MTNNG_CLOUD_AZ_1`).",
        "",
        "- Settings marked *master tenant only* refuse to plan for other tenants.",
        "- Customer-facing names are used throughout: **MTN Network**, **MTN Router**, **MOS** "
        "(MTN Object Storage).",
        "",
        "## Resources",
        "",
    ]
    for title, intro, names in RESOURCES:
        out += [f"### {title}", "", intro, ""]
        out += [", ".join(f"[`mtncloud_{n}`](../resources/{n}.md)" for n in sorted(names)), ""]
    out += ["## Data sources", ""]
    for title, names in DATA_SOURCES:
        out += [f"### {title}", ""]
        out += [", ".join(f"[`mtncloud_{n}`](../data-sources/{n}.md)" for n in sorted(names)), ""]
    out += [
        "## Coming next",
        "",
        "Designed and in progress. These are **not available yet**. Until `mtncloud_instance` ships, "
        "virtual machines can't be created with this provider. Load balancers are on hold.",
        "",
    ]
    for title, items in PLANNED:
        out += [f"### {title}", "", "| Type | What it does |", "|---|---|"]
        out += [f"| `{n}` | {d} |" for n, d in items]
        out += [""]
    OUT.parent.mkdir(parents=True, exist_ok=True)
    OUT.write_text("\n".join(out), encoding="utf-8", newline="\n")
    print(f"gen-features: wrote {OUT.relative_to(OUT.parents[2])} ({nr} resources, {nd} data sources)")


main()
