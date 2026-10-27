# Changelog

All notable changes to this provider. Format: [Keep a Changelog](https://keepachangelog.com), versions: SemVer.

## Unreleased

- Docs: the examples now pin `~> 0.3`.
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
