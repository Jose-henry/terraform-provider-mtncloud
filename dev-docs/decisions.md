# Decisions

Each entry records what was decided, why, and what it rules out. Change a decision by adding a new
entry that supersedes it, never by editing history.

## D-001 Fork the upstream provider, rename everything (2026-10-26)
Start from the upstream Morpheus SDKv2 provider (MIT), moved whole into `internal/legacy`. The
product name is replaced everywhere: types `mtncloud_*`, env vars `MTNCLOUD_*`, docs "MTN Cloud".
Exceptions: the external SDK import (aliased `sdk`), the LICENSE notice, and API wire values marked
`mtncloud:allow-upstream-name`. Enforced by `scripts/check-names.sh` in CI.

## D-002 OpenStack only (2026-10-26)
MTN Cloud runs HPE Morpheus on OpenStack. Removed: AWS, Azure, vSphere, standard/MVM clouds and
instances, ARM and CloudFormation blueprints and spec templates, `cloud_datastore`,
`cloud_folder`. Kept: Kubernetes, Helm and Terraform blueprints and spec templates (not cloud-bound),
all integrations, and all appliance configuration. `TestProvider_RemovedTypesStayRemoved` keeps them
out.

## D-003 Registry `mtn/mtncloud`, code at repo root (2026-10-26)
Go module `github.com/mtn/terraform-provider-mtncloud`. Open: the registry needs a public GitHub
repo named `terraform-provider-mtncloud` under the publishing org, and the module path must match
it. Rename before the first release (Q5).

## D-004 Master-only features refuse at plan time (2026-10-26)
Appliance configuration resources work only for the master tenant. The provider calls
`/api/whoami` once at configure time. Master-only resources then return a plan-time error that says
which tenant you are logged in as. The API is never left to fail halfway through an apply. The
authoritative list of master-only types is pending from MTN (Q1).

## D-005 New resources use the Plugin Framework, muxed with legacy (2026-10-26, confirmed)
New and rewritten resources are written with terraform-plugin-framework and served alongside
`internal/legacy` (SDKv2) through terraform-plugin-mux. A resource moves out of legacy only when it
is rewritten with tests. No big-bang migration.

## D-006 Names and codes, not IDs, in user config (2026-10-26)
See [catalog-lookups.md](catalog-lookups.md). Resources resolve readable names and codes at plan
time, list data sources expose the catalog, and no numeric ID is hard-coded in Go.

## D-007 MOS is one resource: keys plus optional buckets (2026-10-26, design)
See [mtn-cloud-domain.md](mtn-cloud-domain.md#mos-mtn-object-storage). Customers see "MOS",
never "S3" (apart from "S3-compatible"). Key rotation is tenant-wide, so keys are adopted, never
silently re-ordered.

## D-008 Customer names for networking (2026-10-26)
Resources are `mtncloud_network` and `mtncloud_router`. Docs call them **MTN Network** and
**MTN Router**.

## D-009 Branch, PR, green CI, then merge (2026-10-26)
The first scaffold commit went straight to `main` (no code). From then on: work on a branch, open a
PR, and merge only when CI is green. Acceptance tests against MTN Cloud run only with explicit
approval and dedicated test credentials.

## D-010 Restore `mtncloud_cloud_datastore`; datastores are Cinder volume types (2026-10-26)
D-002 removed `cloud_datastore` as vSphere-only. The CLI capture (`datastores list`) shows that on
MTN Cloud every datastore is type `generic` on cloud `MTNNG_CLOUD_AZ_1`: one per project
(`__DEFAULT__`), plus `ENCR_AT_REST` (encrypted) and `_MULTIATTACH_` (shared volumes). These are the
OpenStack Cinder volume types. The owner wants all of them selectable, and more may be added later,
so nothing is hard-coded: users pick a datastore by **name** and the ID is resolved per project.
- The legacy data source is restored as-is (lookup by name or ID within a cloud).
- Planned `mtncloud_datastores` lists them for a project; `mtncloud_instance` volumes take
  `datastore = "<name>"` (default: the project's `__DEFAULT__`, sent as `datastoreId: "auto"`) with
  storage type `openstackVolume` (id 11). `openstackLocal` (id 12, not resizable) is the flavor disk.

## D-011 Images are OS instance types only; apps come from the catalog (2026-10-26)
`mtncloud_images` returns public OpenStack instance types with category `os`. App types (MK8S
master/worker, LAMP, Fortinet and similar) are ordered through catalog items; their instance types
are resolved internally only to build catalog orders.

## D-012 An instance takes several security groups (2026-10-26)
`security_groups` is a set of names, sent as `securityGroups: [{"id": N}, ...]`, as MTN's
FastCreateLinuxInstance.sh does. Order has no meaning, so it is a set (no diff churn).

## D-013 Load balancer is on hold (2026-10-26)
The owner has paused load balancer work. Captures show Octavia (type `octavia`) in use, but no LB
resource is designed or documented until the owner reopens it.

## D-014 Certificate verification is on by default (2026-10-26)
Owner's decision. `secure` now defaults to `true` in both mux halves; `secure = false` or
`MTNCLOUD_API_SECURE=false` opts out, with a warning on every run. Before this, the inherited
default skipped verification. Users of a console with a self-signed certificate must now set
`secure = false`: call it out in the first release notes.

## D-015 Provider `url` is Optional in the schema, checked in Configure (2026-10-26)
SDKv2 turns `Required` + `DefaultFunc` into Optional only when `MTNCLOUD_API_URL` is set, so the
schema changed with the environment and the Framework half could never match it reliably (the mux
needs identical provider schemas). Both halves declare `url` Optional and fail in Configure with a
clear message when neither the argument nor the env var is set. `TestMux_ProviderSchemasMatch`
guards the match.

## D-016 Lookups use the customer-safe endpoints (2026-10-26)
See [catalog-lookups.md](catalog-lookups.md). Admin endpoints return 403 for customer roles, so
every lookup uses the provisioning wizard's endpoints, verified with a sub-tenant login.

## D-017 MTN Network DNS defaults to MTN's resolvers (2026-10-26)
Owner's request. Customers almost always type MTN's resolvers into the console's create-network form
(it prints them as hints under the fields). `mtncloud_network` will declare `dns_primary` and
`dns_secondary` as **Optional with a schema default**: `102.88.134.41` and `102.90.109.125`. Users can
override either one, and neither is required. A default (not Optional+Computed) makes the plan show
the value that will be sent, and keeps the empty plan after apply. Validate both as IPv4 at plan time.
Defaults live in one constant pair in the network service package, with a comment pointing here, so a
resolver change is a one-line edit plus a CHANGELOG note (changing a default produces a diff for
existing networks that didn't set the attribute).

## D-018 The cloud is discovered, not hard-coded (2026-10-26)
MTN Cloud has one OpenStack cloud, `MTNNG_CLOUD_AZ_1` (id 4 today). Customers get 403 on
`/api/zones` but can read `/api/options/clouds`, which returns it. The provider discovers the
group and the cloud once per run (`client.Placement`) and errors, listing the choices, if there is
ever more than one. No cloud ID is written in Go (rule 4). Planned: an optional provider argument
`cloud` (by name) for when MTN adds a second availability zone. The registry docs name
`MTNNG_CLOUD_AZ_1` so users know what they're on. See [customer-permissions.md](customer-permissions.md).

## D-019 Security group default egress rules: kept, visible, removable, never waited for (2026-10-26)
OpenStack adds two allow-all egress rules (`0.0.0.0/0`, `::/0`) to every new group. MTN Cloud only
shows them after its sync with OpenStack, which can take a long time. Owner's direction: leave them in
place by default, let users see every rule and remove the defaults if they want, and **never poll for
the sync**. So:
- Default egress rules are recognised by their synced shape (`ruleType` `custom`, egress, any
  protocol, no port, `0.0.0.0/0` or `::/0`) and are never counted as drift of `rules`. A user's own
  allow-all rule is made through the API as `customRule`, so it never matches.
- `all_rules` (computed) lists every rule, with `default = true` on these two.
- `remove_default_egress = true` removes them. Because they arrive later, ModifyPlan marks
  `all_rules` unknown whenever the state shows a default, so the first apply after the sync deletes
  them. Before the sync there is nothing to delete and the plan is empty.
- Declaring a rule shaped like a default is a plan-time error (Neutron refuses duplicates).
- Consequence, documented on the attribute and in the example: a group with
  `remove_default_egress = true` still allows all outbound traffic from create until the first apply
  after the sync. Closing that gap would need a wait for the sync, which the owner ruled out.
- Create never adopts a group it didn't make: it refuses a name already used in the project (and
  points at import), and only after a lost reply (transport error or 5xx) looks the group up again.

## D-020 Catalog orders wait for their execution or provisioning, never for the sync (2026-10-26)
Owner's direction, for `mtncloud_project`, MOS and every catalog-order resource. Nothing ever waits
for what an order makes to sync into MTN Cloud (the project's resource pool, its default security
group): "they'll come when they do", and are read when they appear, like the default egress rules
(D-019). Networks and routers are ready when their create call returns.

What Create waits for depends on the inventory item the order creates (`/api/catalog/items/{id}`,
its `type.type`), from the API reference (`addcatalogorder`, `getcatalogitem`):
- **Workflow items** (project, MOS, VPN, firewall, NAT: API task executions): poll the item's
  execution to a terminal state, success or failure, and fail with the platform's own message.
- **Instance items**: poll the item until it links its instance, then the instance's provisioning
  status to `running` (done) or `failed`. The item's own status can stay "ordered" while the VM is
  still provisioning, so it alone isn't enough.
- **App (blueprint) items**: the same, with the app's status, which covers all its instances.

One poller in `internal/client` with exponential backoff and jitter, honouring `timeouts {}` and
`ctx`. To confirm before building: the item status values and where a workflow item exposes its
execution (a read of `/api/catalog/items` on an existing order, as a customer).

## D-021 Public registry address `jose-henry/mtncloud` (2026-10-27)
Supersedes the registry part of D-003. The owner chose the public Terraform Registry, where the
namespace is the GitHub owner of the repository (`github.com/Jose-henry/terraform-provider-mtncloud`).
Users write `source = "jose-henry/mtncloud"`; `main.go` serves `registry.terraform.io/jose-henry/mtncloud`.
The Go module path (`github.com/mtn/terraform-provider-mtncloud`) is internal and unchanged. Moving the
repo to an MTN GitHub org later changes the address, so users would update `source`; decide that
before many people depend on it.

## D-022 `mtncloud_project`: the order is the resource; no change, no destroy (2026-10-27)
Owner's choices. MTN's catalog has "Create Project" (type 10, a workflow on the appliance) and no
"Delete Project". Its workflow (`Project-ResourcePool.sh` in MTN-Cloud-Service-Automation) POSTs a
resource pool named `<projectName>-<tenant>` to `/api/zones/{cloud}/resource-pools`.
- **ID** is the catalog order item (`/api/catalog/items/{id}`), which exists the moment the order
  is placed; the pool only appears after the workflow and the sync. The item doesn't show the order's
  form, so `name`/`description` are kept as configured.
- **Create** orders, then polls the item's execution to success or error (D-020). Nothing waits for
  the sync: the docs tell users to allow up to an hour for the default security group, networks and
  floating IP pools. A failed execution keeps nothing (nothing was made); a result not known by the
  timeout keeps the order with a warning, so a retry never orders a second project.
- **Changes** to name or description are plan-time errors (the owner chose this over replace).
- **Destroy** is refused with an error that names the `removed` block and `terraform state rm`.
- Existing project items read `execution: null`, `status: ORDERED`. If new orders behave the same,
  Create ends at its timeout with the warning; B-015 checks this live.

## D-023 Load balancer back on the plan; cluster and datastore added (2026-10-27)
Owner's direction. Reopens D-013: `mtncloud_load_balancer` is planned again (Octavia; its API still
needs a capture before design). `mtncloud_cluster` and `mtncloud_datastore` join the planned
resources. Each needs the usual research (MTN Drive docs, Context7, a read-only capture) and an
approved plan before code.

## D-024 Master-only types removed (2026-10-27)
Owner's direction: the provider is for customers (sub-tenants), so the 36 resources and 12 data
sources that only the master tenant can use are removed: tenants and tenant roles, appliance,
backup, guidance, monitoring and provisioning settings, licenses, cluster packages, resource pool
groups, every policy, service plans, prices and price sets; and the cloud(s), cloud datastore,
resource pool, plan, policy/policies, price, price set and tenant(s) data sources. Supersedes the
master-only half of D-004 (master gating is no longer needed for these). `TestProvider_RemovedTypesStayRemoved`
keeps them out. The owner audits the remaining types next and names further removals.

## D-025 Owner's audit of the remaining types (2026-10-27)
Customers' Library holds automation, virtual images and integrations, plus forms, inputs and
option lists. Removed: instance types, layouts, node types, cluster layouts, templates, scripts,
security packages, blueprints, spec templates and catalog items (resources and lookups), the
Groovy/JavaScript/Python/jRuby task types their console doesn't offer, the cloud type and
provision type lookups, and the `cypher_secret` data source (replaced by `mtncloud_cypher`). Kept:
identity sources (customers use them). The full audit, answers and phased plan are in
[audit-2026-10-27.md](audit-2026-10-27.md).

## D-027 Provider-wide project, group and cloud (2026-10-28)

`project`, `group` and `cloud` on the provider block, or `MTNCLOUD_PROJECT`, `MTNCLOUD_GROUP`,
`MTNCLOUD_CLOUD` (owner: tfvars or env, no profile file). Order: the resource's value, then the
provider block, then the environment. None set is a plan-time error naming all three places.
- Resources: `project` on instance, network, router and security group; `group` on instance. They
  are Optional+Computed, and state keeps the value used, so a new default shows as a replacement.
- The fill is in each resource's `ModifyPlan` (`planmodifiers.FillDefault`), not an attribute plan
  modifier: the framework builds the schema on an unconfigured resource instance, so a modifier
  can't reach the client (found by test, 2026-10-28).
- `group`/`cloud` also choose among several when the account has more than one (`client.pick`);
  with one of each, nothing changes.
- Data source filters do **not** take the defaults: a filter is something the reader asks for, and
  a list that silently narrowed to the provider's project would hide resources.
- The legacy half declares the same three attributes so the mux schemas match; it doesn't read them.

## D-026 One typed resource per catalog item (owner, 2026-10-27)
Each orderable catalog item gets its own resource, named after the service (`mtncloud_mysql`,
`mtncloud_managed_database`, `mtncloud_kubernetes_cluster`, ...), not a generic order with an
inputs map: state then holds what the order created, so plan shows drift, destroy removes the right
thing, import works, inputs are validated at plan time, and each has its own docs page. One shared
core places the order, polls its execution, finds what it created and waits for it; two families:
execution only (project, MOS, NFS, SMB, Let's Encrypt SSL) and execution plus instance provisioning
(the items that deploy VMs). The shared VM inputs use `mtncloud_instance`'s attribute names.
Passwords are write-only arguments (never in state; Terraform 1.11 or newer, documented).
Not built: "Create Instance" (`mtncloud_instance` does it), "Trouble Ticketing" (not
infrastructure), and a generic `mtncloud_catalog_order`. Inputs per item:
[catalog-forms.md](catalog-forms.md). Each resource's order is confirmed with a CLI dry run first.

