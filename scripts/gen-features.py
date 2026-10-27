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
    ("Appliance configuration",
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
     "Quotas, approvals, naming and other guard rails. The master tenant applies these to "
     "sub-tenants: customers can't read or manage policies.",
     ["backup_creation_policy", "budget_policy", "cluster_resource_name_policy",
      "cypher_access_policy", "delayed_delete_policy", "delete_approval_policy", "hostname_policy",
      "instance_name_policy", "max_containers_policy", "max_cores_policy", "max_hosts_policy",
      "max_memory_policy", "max_storage_policy", "max_vms_policy", "motd_policy",
      "network_quota_policy", "power_schedule_policy", "provision_approval_policy",
      "router_quota_policy", "tag_policy", "user_creation_policy", "user_group_creation_policy",
      "workflow_policy"]),
    ("Plans and pricing",
     "Service plans and the prices attached to them. Master tenant only; customers pick sizes "
     "with the `mtncloud_plans` data source.",
     ["service_plan", "price", "price_set"]),
    ("Networking",
     "MTN Networks, security groups (instance firewalls) and their rules, IP pools (static IP "
     "ranges for a network) and DNS domains (instance hostnames and domain join). The MTN Router "
     "resource is next (see below).",
     ["network", "security_group", "security_group_rule", "ipv4_ip_pool", "network_domain"]),
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

# Who can use each type with a customer (sub-tenant) login. Evidence: dev-docs/customer-permissions.md
# (read-only probe as a Customer Admin User). Master-only types also say so in their description.
MASTER_ONLY = {
    "resource": {"tenant", "tenant_role", "resource_pool_group", "cluster_package", "license",
                 "appliance_setting", "backup_setting", "guidance_setting", "monitoring_setting",
                 "provisioning_setting", "service_plan", "price", "price_set",
                 "backup_creation_policy", "budget_policy", "cluster_resource_name_policy",
                 "cypher_access_policy", "delayed_delete_policy", "delete_approval_policy",
                 "hostname_policy", "instance_name_policy", "max_containers_policy",
                 "max_cores_policy", "max_hosts_policy", "max_memory_policy", "max_storage_policy",
                 "max_vms_policy", "motd_policy", "network_quota_policy", "power_schedule_policy",
                 "provision_approval_policy", "router_quota_policy", "tag_policy",
                 "user_creation_policy", "user_group_creation_policy", "workflow_policy"},
    "data": {"tenant", "tenants", "tenant_role", "cloud", "clouds", "cloud_datastore",
             "resource_pool", "plan", "policy", "policies", "price", "price_set"},
}
# Built for customers on the endpoints the console's own wizard uses (D-016).
FOR_CUSTOMERS = {"resource": {"network", "security_group", "security_group_rule"},
                 "data": {"projects", "images", "plans", "datastores", "security_groups",
                          "external_networks", "catalog_items"}}
# Need an integration first; the probed tenant had none.
NOT_CHECKED = {"data": {"ansible_tower_inventory", "ansible_tower_job_template", "chef_server",
                        "servicenow_workflow", "vro_workflow"}}
NO_API = {"data": {"permission_set"}}

LABELS = {
    "for": "Yes, built for customers",
    "read": "Yes (read verified)",
    "master": "No: master tenant only",
    "unchecked": "Not checked yet",
    "noapi": "Yes (no API call)",
}


def access(kind, name):
    for key, table in (("master", MASTER_ONLY), ("for", FOR_CUSTOMERS), ("unchecked", NOT_CHECKED), ("noapi", NO_API)):
        if name in table.get(kind, ()):
            return key
    return "read"


# New MTN-specific types: designed, not yet built. Kept here so the guide shows the roadmap.
# (type, status, what it does). Status: Next, Planned or On hold.
PLANNED = [
    ("Resources", [
        ("mtncloud_router", "Next", "MTN Router: connects MTN Networks to each other and, optionally, to the internet through a public network."),
        ("mtncloud_instance", "Planned", "OpenStack VM from an MTN OS image: plan by size name (for example `G2S4`), project, networks, several security groups, volumes on any Cinder datastore, optional floating IP."),
        ("mtncloud_floating_ip", "Planned", "A public IP that can outlive an instance."),
        ("mtncloud_project", "Planned", "Order a project (resource pool) by name and wait until it and its default security group have synced."),
        ("mtncloud_catalog_order", "Planned", "Order any catalog service (Kubernetes, managed databases, VPN, firewall, NAT, BYOL appliances and more), with its inputs."),
        ("mtncloud_object_storage", "Planned", "MOS (MTN Object Storage): S3-compatible keys plus optional buckets, in one resource."),
        ("mtncloud_app", "Planned", "Apps from blueprints: several instances or containers deployed together."),
        ("mtncloud_ssl_certificate", "Planned", "Certificates."),
        ("mtncloud_load_balancer", "On hold", "Load balancers (OpenStack Octavia)."),
    ]),
    ("Data sources", [
        ("mtncloud_routers", "Planned", "MTN Routers in a project."),
    ]),
]


def main():
    registered = {"resource": set(), "data": set()}
    described_master = {"resource": set(), "data": set()}  # description starts "Master tenant only"
    for line in sys.stdin:
        kind, name, flag = line.split()
        name = name.removeprefix("mtncloud_")
        registered[kind].add(name)
        if flag == "master":
            described_master[kind].add(name)

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
    for table in (MASTER_ONLY, FOR_CUSTOMERS, NOT_CHECKED, NO_API):
        for kind, names in table.items():
            if names - registered[kind]:
                errors.append(f"{kind}: access map names unregistered types: {sorted(names - registered[kind])}")
    for kind in registered:
        # The guide and each type's own docs must agree on master-only.
        if MASTER_ONLY[kind] - described_master[kind]:
            errors.append(f"{kind}: master-only here but the description doesn't start with "
                          f"'Master tenant only': {sorted(MASTER_ONLY[kind] - described_master[kind])}")
        if described_master[kind] - MASTER_ONLY[kind]:
            errors.append(f"{kind}: description says master-only but MASTER_ONLY doesn't: "
                          f"{sorted(described_master[kind] - MASTER_ONLY[kind])}")
    if not registered["resource"] or not registered["data"]:
        errors.append("no input: pipe `go run ./tools/listtypes` into this script")
    if errors:
        sys.exit("gen-features: " + "\n".join(errors))

    nr, nd = len(registered["resource"]), len(registered["data"])
    mr, md = len(MASTER_ONLY["resource"]), len(MASTER_ONLY["data"])
    ur, ud = len(NOT_CHECKED.get("resource", ())), len(NOT_CHECKED.get("data", ()))
    out = [
        "---",
        'page_title: "Features"',
        "description: |-",
        "  Everything the MTN Cloud provider can manage today, who can use it, and what is coming next.",
        "---",
        "",
        "# Features",
        "",
        "<!-- Generated by scripts/gen-features.py from the provider's registered types. Do not edit. -->",
        "",
        "The provider targets MTN Cloud on OpenStack. The cloud (`MTNNG_CLOUD_AZ_1`) is found "
        "automatically: you never configure it.",
        "",
        "| | Resources | Data sources |",
        "|---|---|---|",
        f"| Available today | {nr} | {nd} |",
        f"| Usable with a customer (sub-tenant) login | {nr - mr - ur} | {nd - md - ud} |",
        f"| Master tenant only | {mr} | {md} |",
        f"| Not checked yet with a customer login | {ur} | {ud} |",
        f"| Coming next | {len(PLANNED[0][1])} | {len(PLANNED[1][1])} |",
        "",
        "## Who can use what",
        "",
        "MTN Cloud customers log in to a **sub-tenant**. The platform hides some things from them "
        "(clouds, hosts, prices, policies, other tenants), so the **Customer login** column says, "
        "for each type:",
        "",
        f"- **{LABELS['for']}**: made for customers. It uses the same calls as the console's own "
        "screens, checked with a customer login.",
        f"- **{LABELS['read']}**: a customer login can read what it manages. Creating and changing "
        "are confirmed type by type as acceptance tests are added.",
        f"- **{LABELS['master']}**: customer logins are refused (HTTP 403). Only the MTN Cloud master "
        "tenant can use it, and its description says so.",
        f"- **{LABELS['unchecked']}**: needs an integration to exist first; not yet tested with a "
        "customer login.",
        f"- **{LABELS['noapi']}**: builds a value locally and never calls MTN Cloud.",
        "",
        "Customer-facing names are used throughout: **MTN Network**, **MTN Router**, **MOS** "
        "(MTN Object Storage).",
        "",
        "## Resources",
        "",
    ]

    def table(kind, folder, names):
        rows = ["| Type | Customer login |", "|---|---|"]
        rows += [f"| [`mtncloud_{n}`](../{folder}/{n}.md) | {LABELS[access(kind, n)]} |" for n in sorted(names)]
        return rows + [""]

    for title, intro, names in RESOURCES:
        out += [f"### {title}", "", intro, ""] + table("resource", "resources", names)
    out += ["## Data sources", ""]
    for title, names in DATA_SOURCES:
        out += [f"### {title}", ""] + table("data", "data-sources", names)
    out += [
        "## Master tenant only",
        "",
        "These need the MTN Cloud master tenant. A customer login gets HTTP 403.",
        "",
        "- **Resources:** " + ", ".join(f"`mtncloud_{n}`" for n in sorted(MASTER_ONLY["resource"])),
        "- **Data sources:** " + ", ".join(f"`mtncloud_{n}`" for n in sorted(MASTER_ONLY["data"])),
        "",
        "Customers use `mtncloud_projects` instead of `mtncloud_resource_pool`, `mtncloud_plans` "
        "instead of `mtncloud_plan`, and `mtncloud_datastores` instead of `mtncloud_cloud_datastore`. "
        "They don't need `mtncloud_cloud`: the provider finds the cloud.",
        "",
        "## Coming next",
        "",
        "Designed, **not available yet**, and all meant for customers. Until `mtncloud_instance` "
        "ships, virtual machines can't be created with this provider.",
        "",
    ]
    for title, items in PLANNED:
        out += [f"### {title}", "", "| Type | Status | What it does |", "|---|---|---|"]
        out += [f"| `{n}` | {st} | {d} |" for n, st, d in items]
        out += [""]
    OUT.parent.mkdir(parents=True, exist_ok=True)
    OUT.write_text("\n".join(out), encoding="utf-8", newline="\n")
    print(f"gen-features: wrote {OUT.relative_to(OUT.parents[2])} ({nr} resources, {nd} data sources)")


main()
