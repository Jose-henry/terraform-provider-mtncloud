# Changelog

All notable changes to this provider. Format: [Keep a Changelog](https://keepachangelog.com), versions: SemVer.

## Unreleased

- Forked from the upstream provider (MIT) and renamed to `mtncloud` (`mtn/mtncloud`).
- Scope reduced to OpenStack: removed AWS, Azure, vSphere, standard and MVM clouds and instances, ARM and CloudFormation blueprints and spec templates, and the `cloud_folder` data source. `cloud_datastore` was restored: datastores are OpenStack Cinder volume types.
- Environment variables renamed to `MTNCLOUD_API_*`.
- **Breaking:** TLS certificate verification is now on by default (`secure` defaults to `true`). For a console with a self-signed certificate, set `secure = false` or `MTNCLOUD_API_SECURE=false`.
- `url` is optional in the schema (it can come from `MTNCLOUD_API_URL`); a clear error is shown when neither is set.
- Sub-tenant login now sends `subdomain\username` with a single backslash.
- New data sources that pick things by name, using endpoints customer roles can call: `mtncloud_projects`, `mtncloud_images`, `mtncloud_plans`, `mtncloud_datastores`, `mtncloud_security_groups`, `mtncloud_external_networks`, `mtncloud_catalog_items`.
- The provider now serves the legacy and Plugin Framework implementations together (terraform-plugin-mux).
- The provider address is `jose-henry/mtncloud` on the public Terraform Registry (D-021).
- New resource `mtncloud_router` (MTN Router): connects networks of a project, with an optional gateway; waits until ready, with `timeouts`.
- New resource `mtncloud_network` (MTN Network): project by name, DNS defaulting to MTN's resolvers, optional IPv6, and the subnet ID for routers.
- API refusals sent as a 2xx response with `"success": false` are now reported as errors with MTN Cloud's message.
- New resources `mtncloud_security_group` (rules as a set, every rule visible in `all_rules`, optional removal of the default allow-all egress rules) and `mtncloud_security_group_rule`. Both work with a customer (sub-tenant) login.
- 36 resources and 12 data sources that MTN Cloud only allows for the master tenant now say so in the first line of their description, and the features guide shows which types a customer (sub-tenant) login can use.
