# Backlog

## Open questions (blocking design)

| # | Question | Blocks |
|---|---|---|
| Q1 | Authoritative list of master-tenant-only features | D-004 gating |
| ~~Q2~~ | ~~Confirm Plugin Framework + mux (D-005)~~ resolved: confirmed and built | |
| Q3 | Load balancer API (instance type 70 "LoadBalancer", Octavia, no layouts: custom API?) | `mtncloud_load_balancer` (**on hold**, D-013) |
| Q4 | Payloads P1–P7 in [api-research.md](api-research.md) and P8–P10 in [prerequisites.md](prerequisites.md) | network, router, SG, instance, MOS |
| ~~Q5~~ | Answered (D-021): public registry, `jose-henry/mtncloud` | |
| Q6 | Test tenant + credentials for acceptance tests (isolated, disposable) | acceptance CI |

## Build order (after approval)

1. ~~`internal/client`~~ done.
2. ~~Catalog data sources~~ done: projects, images, plans, datastores, security groups, external networks, catalog items.
3. `mtncloud_security_group` (+ rule)
4. `mtncloud_network` (MTN Network)
5. `mtncloud_router` (MTN Router)
6. `mtncloud_instance` (OpenStack), with an optional floating IP
7. `mtncloud_project`
8. `mtncloud_catalog_order`
9. `mtncloud_object_storage` (MOS)
10. `mtncloud_app`, `mtncloud_load_balancer`, `mtncloud_ssl_certificate`

## B-001 Real bugs inherited in legacy
- 32 `json.Unmarshal` calls whose error is dropped: a bad API response becomes an empty struct, and
  Terraform then plans a recreate. Fix when each resource is migrated.
- 2 unchecked `UpdateInstanceTypeLogo` / `UpdateCatalogItemLogo` calls: the logo upload fails silently.

## B-002 Legacy lint debt (baseline 2026-10-26, only shrinks)
1439 unchecked `d.Set`, 60 QF1008, 1 SA6005, 2 unused funcs. These are excluded for
`internal/legacy/` in `.golangci.yml`. New code gets no exclusions.

## B-003 Upstream examples with schema errors
22 example files use arguments the schema doesn't have (mostly `apply_each_user` on `*_policy`
role examples, `message_type` on motd, `visibility` on the terraform blueprint, a typo
`default_chedefault_checked` on form, and the plan data source missing `provision_type`). They are
listed in `scripts/examples-known-defects.txt`. `scripts/validate-examples.sh` fails if the list
grows or if a listed file becomes clean.

## B-004 Quickstart `terraform.tfvars` (local only)
The local, git-ignored file still has `username`/`password`/`tenant_subdomain` keys that the
quickstart no longer declares (Terraform warns). Remove them locally.

## B-005 TLS verification was off by default (resolved, D-014)
Now on by default in both halves; `secure = false` opts out with a warning.

## B-006 Legacy sub-tenant login sent two backslashes (fixed)
`internal/legacy/config.go` built the username with the raw string `%s\\%s`, sending
`tenant\\user`. The API reference and the owner's CLI login (`acme\jdoe`) use one backslash.
Fixed in legacy and the new client. Confirm with a real sub-tenant run once acceptance tests exist.

## B-007 Legacy types that fail for customer logins (resolved: marked master-only)
36 resources and 12 data sources call endpoints a customer role gets 403 on
([customer-permissions.md](customer-permissions.md)). Each description now starts with "Master tenant
only", pointing customers to `mtncloud_projects`, `mtncloud_plans` and `mtncloud_datastores` where
one exists. The features guide shows a "Customer login" column; `scripts/gen-features.py` fails if
its `MASTER_ONLY` list and the descriptions disagree. Still to do: the plan-time refusal D-004
describes (`internal/tenancy`), so a customer gets a clear error before the API's 403.

## B-008 Apps (multi-instance) and customer inputs
- Blueprint catalog items provision **apps** (several instances or containers). Planned
  `mtncloud_app` (build step 10) and `mtncloud_catalog_order` (step 8) must cover them. `/api/apps`
  and `/api/blueprints` are readable by customers.
- Inputs (option types), option lists and forms: legacy has a resource per input type (text,
  select list, checkbox, number, password, radio list, textarea, typeahead, hidden) plus
  `mtncloud_option_list` and `mtncloud_form`. A customer can read `/api/library/option-types`, but
  the master's inputs aren't shared with the tenant. Catalog orders need each item's inputs: expose
  them on `mtncloud_catalog_items` (name, type, required, default, choices) when the order resource
  is designed.

## B-009 Security group behaviour still to prove live (acceptance run, test tenant)
The live test (2026-10-26) made two CIDR rules and saw no sync. These are assumed by the fake API
and need an acceptance run, or a read of a real group that has such rules:
- Rules with no `cidr` (`sourceType all`): is `destination` filled on egress? If it reads back as
  `0.0.0.0/0`, a configured `{egress, tcp, 443}` would churn. Is `{egress, any}` with no remote
  refused as a duplicate of the IPv4 default?
- Group-source rules, `any` and `icmp` rules, and an unnamed rule: how they are echoed.
- `PUT` with `description: ""`: does it clear the description?
- Deleting a synced default egress rule: does the next sync bring it back? If so,
  `remove_default_egress` never settles.
- A group with several locations: `project` is read from the first one.
- Tests still missing: a failure partway through Update, the rule resource's disappears and
  drift steps, and the acceptance set (`_basic`, `_update`, `_import`, `_disappears`, `_drift`).

## B-010 MTN Network DHCP allocation and host routes
The network type offers `config.dhcpRange` ("DHCP Allocation") and `config.hostRoutes`, and MTN's
doc lists both as optional, but no network in the probed tenant sets them and no document gives the
format. Add `dhcp_range` and `host_routes` to `mtncloud_network` once a real value is captured
(create one in the console on a test network, then `networks get`).

## B-011 MTN Network behaviour still to prove live (acceptance run, test tenant)
Assumed by the fake API; no IPv6 network or failed network has been captured.
- `PUT` with `description: ""` (or no IPv6 DNS): does it clear the value? If not, removing it
  plans forever.
- IPv6: does the API fill a default IPv6 DNS when none is sent, and does it honour `ipv6Enabled`?
  Either would give "inconsistent result" on apply.
- The status words of a failed network (only `ok` was seen; `failed`, `error`, `deprovisioned` are
  treated as failed).
- Changing the gateway while a router port holds the old gateway address: refused by Neutron? If
  so, make `gateway` RequiresReplace or document it with `mtncloud_router`.
- The status code of a security group "in use" refusal (the hint now covers 400, 409 and 2xx).

## B-012 MTN Router behaviour still to prove live
- Straight after a PUT, does GET already show the new interfaces, or "ok" with the old ones for a
  moment? If the latter, update must wait for the interfaces to match, or apply fails with
  "inconsistent result".
- Does re-sending the same `externalNetwork` keep the external IP? (Update never does it, so this
  only matters for documentation.)
- Is the gateway interface's `ipAddress` set when the status turns "ok", or a moment later?
- Router status words besides `provisioning`, `ok` (a failed router wasn't seen), and whether the
  GET carries a status message to show with them.
- `external_network` written in another case than MTN Cloud's (or a full project name) gives one
  no-op update after an import.
