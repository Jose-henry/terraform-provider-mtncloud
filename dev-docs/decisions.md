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


## D-028 Scope after the owner's review of phase 4 (2026-10-28)

- **Integrations are set up in the console.** The eight integration resources (Ansible, Ansible
  Tower, Chef, Docker registry, Git, Puppet, ServiceNow, vRO) are removed and kept out by
  `TestProvider_RemovedTypesStayRemoved`. The read-only lookups stay (`mtncloud_integration(s)`,
  `git_integration`, `ansible_tower_inventory`, `ansible_tower_job_template`, `chef_server`,
  `servicenow_workflow`, `vro_workflow`): the remaining tasks take integration IDs, and these are
  how a configuration finds them.
- **Catalog orders have no destroy**, exactly like `mtncloud_project`: destroy refuses with an error
  saying where to delete what the order made (the "Managed ..." items under Apps, VM items under
  Instances) and how to stop managing it (`removed` block or `terraform state rm`). Their inputs
  can't change after the order (plan-time error). NFS and SMB, which have their own update and
  delete orders, get real update and delete. MOS stays out until its key-rotation design (D-007) is
  approved: ordering it rotates the tenant-wide key.
- **SSL certificate:** a resource for internal certificates only, for now; the data sources exist.
- **Cluster and load balancer come last.** Clusters: Kubernetes and Docker cluster types only, MTN's
  layouts only. Load balancers follow a different path; the owner will send details first.
- **Tasks:** everything but the type updates in place, the name included (tested).
- **Cypher:** no `ttl`, or `0`, means no expiry (owner). Keys are stored with their size
  (`password/15/x`, `key/256/x`; platform docs via NotebookLM), so the provider uses one form.

## D-030 Kubernetes is self-managed; SIEM isn't a managed service (owner, 2026-10-29)
Owner: "those two are self managed". `mtncloud_managed_kubernetes` is renamed
`mtncloud_self_managed_kubernetes`. It is still an app (destroy goes through its app, D-029). The old
name stays registered as a deprecated alias of the same spec, and the new type implements
`MoveState`, so `moved {}` moves the state with nothing replaced (Terraform 1.8+). Remove the alias in
a later minor release, with a changelog note. `mtncloud_siem` keeps its name. It was never an app; it is
now listed with the security appliances. The spec flag `managed` is renamed `app`, since it means
"deleted through its app", not "managed by MTN".

## D-029 Catalog orders are destroyed through what they made (owner, 2026-10-28)

Reverses the "no destroy" part of D-028. The owner: "when a user does destroy for a catalog order
that under the hood is an app or instance, the destroy should go to delete it from the actual app
or instance api directly". The 29 catalog items' forms were read (captures `au-cat-<id>`): 23 ask
for a cloud, resource pool, plan, disks and networks, so they provision VMs (every typed order
resource except Let's Encrypt, including VPN, WireGuard, the Fortinet appliances and SIEM); the
other six are Trouble Ticketing, Create Project, MOS, SMB, NFS and Let's Encrypt.

- A workflow order's inventory item has `refType: Workflow` and no link to what it made, so
  `DELETE /api/catalog/items/{id}` alone (documented to deprovision linked instances) can't be
  relied on. Destroy deletes the order's instances (`instance_ids`, else found by name in the
  project), waits until they are gone, then deletes the inventory record (a failure there is only
  a warning).
- Managed services, Kubernetes included (owner: "mk8s kubernetes is an app"), are apps: the app
  named after the order and holding at least one of its instances is deleted with
  `removeInstances=on` (a name alone could be another project's app). Any other app is deleted only when
  every instance in it is the order's; a shared app is kept and only the order's instances go.
- Matching by name is exact for a single-VM order; `<name>-<n>` only for managed services' nodes,
  so `db` never claims a sibling order's or the user's `db-2` (review, 2026-10-28).
  Destroy deletes only instances that match now: saved `instance_ids` narrow the name match, so a
  renamed VM, or one an older version claimed, is left alone.
- Destroy refuses, deleting nothing, rather than guess: a lookup that fails, a workflow still
  running past the delete timeout, an import not yet applied (`name` unknown), or nothing found.
- Let's Encrypt makes nothing deletable: destroy only removes it from state, with a warning that
  nothing was deleted (owner's answer).
- Inputs still can't change in place (D-028); `terraform apply -replace` now works, as destroy does.
- Not yet verified live: the managed services' app names and node names (no managed order has been
  placed in the test tenant).

## D-031 Load balancers go through one console task (owner, 2026-10-29)
Supersedes the API part of D-013 and D-023. MTN's load balancers are run by a console plugin, reached
by executing the public task with code `lboperate.terraform` (type `octaviaLbOperations`; id 450 in
production, but the client finds it by code, once per run) with the customer's own token:
`POST /api/tasks/{id}/execute` with `job.customOptions` (`action` plus string inputs) and `targetType:
appliance`, then `GET /api/job-executions/{id}` until it ends; the answer is the `executeTask` event's
`output` envelope (`success`, `action`, `error`, `retryable`, `result`). The plugin enforces tenancy,
so the provider never sends an account or user. Source: the plugin's
`docs/TERRAFORM-AGENT-GUIDE.md` and live reads on 2026-10-29 (`whoami`, `list`, `read`, `fip-options`;
dev-docs/api-research.md).
- The client retries, up to 4 times with growing waits, only runs that did nothing: the console's
  input timeout (`action` null), a job that ended without output, or `retryable: true`. Refusals are
  never retried. "not available to your tenant" (a network the tenant neither owns nor shares) is
  never treated as gone; "not found on this network" is not "deleted".
- A `read` takes 3 to 26 s, so data sources read each load balancer once per run
  (`LoadBalancerSnapshot`) and every listener, pool, member and monitor data source picks from it.
- The old `mtncloud_load_balancer(s)` record lookups read `/api/load-balancers`, which is empty on MTN
  Cloud (checked 2026-10-29); the new data sources replace them under the same names (breaking, noted
  in the CHANGELOG). The resources follow in the next slice.

## D-032 MOS is managed through its S3-compatible API, and buckets are also console records (owner, 2026-10-29)
Builds on D-007 (adopt the tenant's MOS keys, never order new ones). Customers get typed resources for
what MOS supports, not one `mtncloud_object_storage` resource: `mtncloud_mos_bucket`, its settings
(`_acl`, `_cors`, `_lifecycle`, `_logging`, `_object_lock`), `mtncloud_mos_object` and `_object_acl`,
with a data source for every one of them, plus `mtncloud_mos_buckets`, `_objects` and `_credentials`.
Sources: MTN's MOS docs and `RevalidateMOS.sh` (console record shape), the OneFS 9.15 S3 probe, and live
tests on 2026-10-29 (dev-docs/api-research.md, dev-docs/mos-capabilities.md).
- Keys come from Cypher (`secret/MOS-<tenant>_<user>_*`, `<tenant>` built as MTN's workflow builds it),
  or from `MTNCLOUD_MOS_ACCESS_KEY` / `_SECRET_KEY` / `_ENDPOINT`. Environment variables, not provider
  arguments, because both halves of the muxed provider must declare the same provider schema.
- Buckets show in the console (owner: "go with 1"). The console's own create (`createBucket: true`)
  fails on a node that hasn't seen the new bucket yet and leaves it orphaned, so the provider creates
  the bucket in MOS, waits until six reads in a row see it, then registers it with `createBucket:
  false`. A registration failure deletes the new, empty bucket again.
- Deleting a console record silently deletes every archive on it and never deletes the bucket in MOS
  (both seen live). So destroy refuses while an archive uses the bucket, empties it only with
  `force_destroy`, deletes it in MOS, then deletes the record.
- An existing bucket is never adopted on create: import it.
- Every MOS write is confirmed by reading until six reads in a row agree (`settle`), because the
  cluster takes a few seconds to show a change on every node.
- Object lock, retention and checksums are built and documented as normal features (owner: "dont say
  they are coming soon"). Until MTN enables them on the cluster, using them fails with a message that
  says so. What needs hardware or a redesign, and what only AWS has, is listed for the owner in
  dev-docs/mos-capabilities.md, not in customer docs.
- Uploading an object resets its ACL on S3, so a re-upload sends the object's current canned ACL with it.
- Settings writes to one bucket take turns (a per-bucket lock in the client): MOS stores a bucket's
  settings together, and two at once lost one live. A write that never appears is written again.
- Console records are matched on `bucketName` after one list call, not with `phrase`, whose fields
  aren't documented.

## D-033 Load balancer resources change one piece at a time (owner, 2026-10-29)
Builds on D-031. The load balancer and each listener, pool, member and health monitor are separate
resources, written with the plugin's piece actions (`listener-create`, `pool-update`, `member-add`,
`monitor-delete`, ..., and `loadbalancer-update` for the load balancer's own fields), never the
whole-load-balancer `update`, so a stale state can't delete a sibling. Source: the plugin's guide
sections 9.2 to 9.6 and a live write probe on network 114 (dev-docs/api-research.md).
- Every piece takes `network_id` and `load_balancer_id` explicitly: the task needs both, and a
  reference to `mtncloud_load_balancer.x.id` orders the work. Import paths are
  `<network_id>/<load_balancer_id>[/<pool_id>][/<piece_id>]`; a monitor is found by its pool.
- References between pieces are IDs (`default_pool_id`, `pool_id`), not names: piece names are optional
  and not unique, and the reference is what makes Terraform create the pool before its listener and
  delete it after. Names are for what Terraform didn't make: the `mtncloud_load_balancer*` data sources
  resolve a load balancer, pool or listener by name to its ID.
- Writes to one load balancer take turns (a per-load-balancer lock in the client): the plugin refuses
  a second save while one runs. After each write the client polls until the load balancer is no
  longer `PENDING_*`; `ERROR` fails the apply with the cloud's status. Each write drops the run's
  cached reads of that network (B-021).
- A create sends a random `requestId`; the plugin tags the new load balancer `mtncloud-req-<id>` and a
  retried create finds that one. The tag is the plugin's, so it never shows in `tags`.
- A new piece's ID comes from the answer's `piece.id` or, as the live plugin answers, from the step
  of its kind in `steps[]`.
- Name, description, tags and `enabled` change in place with `loadbalancer-update` (added by the
  plugin at the provider's request, 2026-10-29). A load balancer is created enabled; `enabled =
  false` is applied straight after the create. The VIP subnet and address can't change in the cloud:
  they replace it.
- What the plugin can't clear once set (a listener's certificate references, a member's
  health-check address and port) replaces the piece when it is taken out of the configuration. A
  listener has no description: the plugin has none. Optional+Computed settings with a cloud
  default keep their last value when removed, and say so.
- A new piece's ID is looked up on the load balancer when the answer doesn't name it (a monitor by
  its pool, a member by address and port, a listener by protocol and port), also when the write
  failed after the piece was made, so it is kept in state rather than left behind. Writes that may
  have acted are not retried, except `create`, whose requestId makes it safe (B-023).
- The plugin answers a write with `pending: true` while its save job still runs, so an ACTIVE read
  straight after may be the load balancer as it was. Then the wait takes ACTIVE only after a
  PENDING_* read, or after 2 minutes without one.
- Members are external (`address` + `port`) in this slice. Instance and cluster members, L7 policies
  and floating IPs follow.

## D-034 Archives are typed resources on a MOS bucket (owner, 2026-10-29)
The platform's archive API (`/api/archives/buckets`, Context7 reference and the CLI's `archives`
commands; there are no MTN docs for it) is wrapped as `mtncloud_archive_bucket` plus data sources;
files and download links follow (slice 2). Live probe on the dev tenant: dev-docs/api-research.md.
- The bucket is named either way (owner: "use bucket name and id"): `mos_bucket` (its MOS name,
  matched on the console record's `bucketName`) or `mos_bucket_id` (the record ID, what
  `mtncloud_mos_bucket.id` is). Exactly one is set; ModifyPlan resolves the other at plan time, and
  a bucket made in the same run is resolved at create. The API can't move an archive, so another
  bucket replaces it.
- Archives are created private (`visibility: private`, no `accounts`): sharing with other tenants is a
  master-tenant job (D-004) and is left out. `public_url` (`isPublic`) is exposed, with the warning
  that anyone can then download the files.
- Names are unique across every tenant: create looks the name up first and says "import it"; after
  a lost answer it adopts the archive with that name on that bucket (rule 11).
- Deleting an archive deletes its files, also in MOS (live). Destroy refuses while `fileCount > 0`
  unless `force_destroy`, as `mtncloud_mos_bucket` does for objects.
- Slice 2 (2026-10-29): `mtncloud_archive_file`, `mtncloud_archive_file_link`, `mtncloud_archive_files`.
  The API has no checksum, so the file keeps the SHA-256 of what Terraform uploaded and compares the
  config's content with it at plan time; Read only sees a different size (then `content_sha256` is
  emptied and the next apply uploads again). Uploading to the same path keeps the file's ID (live),
  so a content change is an in-place update and links survive it. Create refuses a path that already
  holds a file, since the upload would overwrite it. Links can't be changed, so every argument
  replaces them; `expire_seconds` is Required so that a never-expiring public link is a choice. The
  create answer holds only the secret, so the link is found by it. An expired link is removed from
  state, and the next apply makes a new one. `archive` or `archive_id` (owner: "archive id support
  wont be bad too"), resolved at plan time like `mos_bucket`.
