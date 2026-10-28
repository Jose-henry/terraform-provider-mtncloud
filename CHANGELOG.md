# Changelog

All notable changes to this provider. Format: [Keep a Changelog](https://keepachangelog.com), versions: SemVer.

## Unreleased

- New resource `mtncloud_cluster`: Kubernetes (default) and Docker clusters from MTN's layouts, with master and worker settings, worker count and autoscaling changed in place, and every name checked when you plan.
- New data sources `mtncloud_registries` and `mtncloud_cluster_layouts`.

- New resource `mtncloud_ssl_certificate`: upload a certificate, chain and private key. The key is write-only and is sent again when `private_key_version` changes.

- New resources for MTN's catalog, one per item (D-026): `mtncloud_mysql`, `mtncloud_postgresql`, `mtncloud_mariadb`, `mtncloud_mongodb`, `mtncloud_lamp_stack`, `mtncloud_lemp_stack`, `mtncloud_wordpress`, `mtncloud_apache_web_server`, `mtncloud_nginx_web_server`, `mtncloud_managed_database`, `mtncloud_managed_mq`, `mtncloud_managed_streaming`, `mtncloud_managed_kubernetes`, `mtncloud_siem`, `mtncloud_vpn_service`, `mtncloud_wireguard_vpn`, `mtncloud_bastion_host`, `mtncloud_fortinet_firewall`, `mtncloud_fortiweb`, `mtncloud_fortiadc`, `mtncloud_fortimanager`, `mtncloud_fortianalyzer` and `mtncloud_letsencrypt_certificate`. Every name (project, plan, networks, security groups, floating IP pool, versions and other choices) is checked when you plan. Orders can't be changed or destroyed from Terraform, like projects (D-028); secrets are write-only (Terraform 1.11 or newer).
- New resources `mtncloud_nfs_share` and `mtncloud_smb_share`: created, updated (quota, clients, SMB password) and deleted through their catalog orders.

- **Removed** the integration resources (`mtncloud_ansible_integration`, `mtncloud_ansible_tower_integration`, `mtncloud_chef_integration`, `mtncloud_docker_registry_integration`, `mtncloud_git_integration`, `mtncloud_puppet_integration`, `mtncloud_servicenow_integration`, `mtncloud_vro_integration`): integrations are set up in the console. Run `terraform state rm` on any you manage before upgrading. The integration data sources stay, for tasks that need an integration ID.

- Docs: the examples now pin `~> 0.3`.
- New data sources, singular (by `id` or name) and plural (with `name_regex`): `app(s)`, `ssl_certificate(s)`, `load_balancer(s)`, `cluster(s)`, `catalog_order(s)` (what you ordered, with status), `user(s)`, `identity_source(s)`, `form(s)`; plural only: `user_roles`, `network_subnets`, `storage_buckets`, `storage_volumes`, `domains`, `key_pairs`, `credentials`, `workflows`, `jobs`, `integrations`, `cyphers` (keys and leases, never values); singular only: `project` (with group and cloud), `router`, `security_group` (with every rule), `datastore`, and `cypher` (reads one existing key on any mount, value sensitive; never creates one).
- New resources `mtncloud_cypher_password`, `mtncloud_cypher_uuid` and `mtncloud_cypher_key`: values MTN Cloud generates and keeps in Cypher, with an optional lease (`ttl`). The value is in state, marked sensitive. An existing key is never overwritten, and a refresh never re-creates a deleted one.
- New task resources for the task types the console offers that had none: `mtncloud_ai_task`, `mtncloud_conditional_workflow_task`, `mtncloud_dynamic_wallet_collection_task`, `mtncloud_http_task`, `mtncloud_powerscale_storage_collection_task`, `mtncloud_puppet_agent_install_task` and `mtncloud_reverse_text_task`. AI agents, Puppet masters, workflows and credentials are given by name.
- Provider `project`, `group` and `cloud` (or `MTNCLOUD_PROJECT`, `MTNCLOUD_GROUP`, `MTNCLOUD_CLOUD`): resources that leave out `project` (instance, network, router, security group) or `group` (instance) use them, and `group`/`cloud` choose among several when your account has more than one. State keeps the value used, so changing the provider's `project` later replaces resources that rely on it.
- The same project written another way (`web-prod` or `web-prod-Acme`) on a network, router or security group is no longer a one-time in-place update: the plan is empty.
- List data sources take filters on what the API returns for each object: `status`, `type`, `group`, `cloud`, `visibility`, `environment`, `enabled`, `active` and similar (text matched with case ignored), and `labels` (every one given must be there). `mtncloud_instances` also filters by `image`, `plan` and `tags`, and returns tags; `mtncloud_networks` by `status` and `labels`.
- New data sources `mtncloud_image` (by name or code) and `mtncloud_floating_ips` (the public IPs on your instances; the floating IP endpoint refuses customer logins).
- `mtncloud_instance` and `mtncloud_instances` return `group` and `cloud`.
- **Breaking:** `mtncloud_networks` is rebuilt: `name_regex` and `project` replace `cloud_id`, `sort_ascending` and `filter`; it keeps `ids` and adds `networks`, each with project, group, cloud, CIDR, gateway and status.
- New data source `mtncloud_catalog_item`: one service from the catalog, with the inputs its order form asks for.
- Docs: datastore examples use `__DEFAULT__`, the default datastore.
- **Breaking:** removed what customers can't create in MTN Cloud's Library: instance types, layouts, node types, cluster layouts, file and script templates, boot and preseed scripts, security packages, blueprints, spec templates and catalog items, with their lookups; the Groovy, JavaScript, Python and jRuby script tasks; the cloud type and provision type lookups; and the `mtncloud_cypher_secret` data source (a `mtncloud_cypher` lookup replaces it).
- **Breaking:** removed the 36 resources and 12 data sources only the MTN Cloud master tenant can use (tenants, appliance settings, policies, service plans, prices, licenses, cloud and resource pool lookups and others). Customers pick sizes with `mtncloud_plans` and datastores with `mtncloud_datastores`.
- Fixed: instances made in the console showed an empty `private_ip`.
- Fixed: `mtncloud_plans` accepts the image by name (for example `MTN Ubuntu Server 24.04.3LTS`) as well as by code.
- `mtncloud_network` (data source) is rebuilt: look up by ID or name, optionally in a project; it now returns the network's `project`, `group`, `cloud`, gateway, DNS, DHCP and subnet ID.

## 0.3.0 (2026-10-27)

0.1.0 and 0.2.0 were tagged without their own sections, so this list covers everything since the fork.

- New data sources `mtncloud_routers`, `mtncloud_instances` and `mtncloud_instance`: look up MTN Routers and instances, including those made in the console, by name or ID.
- `mtncloud_instance`: an attach whose address hasn't shown when the timeout ends is never sent again (no second address); a warning shows until it appears. A timeout message now suggests raising `timeouts`.
- Removed the "Sharing Data Between Apps/Instances" guide: it needs master-only secret policies.
- Docs: removed a deprecation notice and an outdated TLS warning carried over from the upstream provider; neither applied to this provider. The front page now describes MTN Cloud, and the examples pin a version.
- Forked from the upstream provider (MIT) and renamed to `mtncloud` (`mtn/mtncloud`).
- Scope reduced to MTN Cloud: removed AWS, Azure, vSphere, standard and MVM clouds and instances, ARM and CloudFormation blueprints and spec templates, and the `cloud_folder` data source. `cloud_datastore` was restored (later removed as master-only).
- Environment variables renamed to `MTNCLOUD_API_*`.
- **Breaking:** TLS certificate verification is now on by default (`secure` defaults to `true`). For a console with a self-signed certificate, set `secure = false` or `MTNCLOUD_API_SECURE=false`.
- `url` is optional in the schema (it can come from `MTNCLOUD_API_URL`); a clear error is shown when neither is set.
- Sub-tenant login now sends `subdomain\username` with a single backslash.
- New data sources that pick things by name, using endpoints customer roles can call: `mtncloud_projects`, `mtncloud_images`, `mtncloud_plans`, `mtncloud_datastores`, `mtncloud_security_groups`, `mtncloud_external_networks`, `mtncloud_catalog_items`.
- The provider now serves the legacy and Plugin Framework implementations together (terraform-plugin-mux).
- The provider address is `jose-henry/mtncloud` on the public Terraform Registry (D-021).
- New resource `mtncloud_project`: orders MTN's "Create Project" catalog item and waits for its workflow, not for the project to sync in (allow up to an hour). Name and description can't change; destroy is refused (use a `removed` block). Import by the catalog order ID.
- `mtncloud_instance` accepts `floating_ip_pool`: a public IP attached once the VM runs, changed in place, shown in `public_ip`. The apply waits for the address and fails if it isn't attached.
- `mtncloud_instance` accepts `scale` (automatic scaling on CPU, memory or disk usage, changed in place) and `expire_days`/`shutdown_days` (set at create; `expire_date` and `shutdown_date` show the dates).
- New resource `mtncloud_instance`: a VM by names (group, project, image, plan, security groups, datastores, environment, domain), checked at plan time. The name is optional (the tenant's naming template applies), data volumes and networks are added in place, a plan change restarts the VM in place, and the project's default security group is always applied. Waits until running, with `timeouts`.
- `mtncloud_network` now waits until MTN Cloud reports a new network ready, and accepts `timeouts { create }`.
- New resource `mtncloud_router` (MTN Router): connects networks of a project, with an optional gateway; waits until ready, with `timeouts`.
- New resource `mtncloud_network` (MTN Network): project by name, DNS defaulting to MTN's resolvers, optional IPv6, and the subnet ID for routers.
- API refusals sent as a 2xx response with `"success": false` are now reported as errors with MTN Cloud's message.
- New resources `mtncloud_security_group` (rules as a set, every rule visible in `all_rules`, optional removal of the default allow-all egress rules) and `mtncloud_security_group_rule`. Both work with a customer (sub-tenant) login.
- 36 resources and 12 data sources that MTN Cloud only allows for the master tenant now say so in the first line of their description, and the features guide shows which types a customer (sub-tenant) login can use.
