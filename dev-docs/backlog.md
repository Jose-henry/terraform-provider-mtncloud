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
2. ~~Catalog data sources~~ done: projects, images, plans, datastores, security groups, external networks, catalog items;
   then routers, instances and instance (2026-10-27).
3. `mtncloud_security_group` (+ rule)
4. `mtncloud_network` (MTN Network)
5. `mtncloud_router` (MTN Router)
6. ~~`mtncloud_instance` (OpenStack)~~ core slice done; floating IP, availability zone, workflow,
   lifecycle, scaling and deployments still to come
7. `mtncloud_project`
8. `mtncloud_catalog_order`
9. `mtncloud_object_storage` (MOS)
10. `mtncloud_app`, `mtncloud_ssl_certificate`, `mtncloud_cluster`, `mtncloud_datastore`,
    `mtncloud_load_balancer` (D-023)

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

## B-013 Instance behaviour still to prove live
- With a fixed instance-name policy, is a `name` sent in the create body ignored, refused, or kept?
  Same question for `hostName` under a fixed hostname policy.
- Does `PUT /api/instances/{id}/resize` accept existing volumes and interfaces as sent (IDs, sizes),
  and does the GET show added ones once the status is back to "running"?
- Does `PUT /api/instances/{id}` with an empty `labels` or `tags` list clear them?
- Delete: does the instance go straight to `pendingRemoval` or 404, or does a delete policy (delayed
  delete) keep it longer than the 30 minute timeout?
- Floating IP with `scale_factor` above 1: the wait and drift check look at the instance's one
  `public_ip`, not each VM's. Does a stopped VM keep its floating IP in `connectionInfo` (if not,
  every plan re-attaches)? Both need the live run.
- Floating IP (built from docs + reads; the apply waits for the address): how long until it shows in
  `connectionInfo`, and is the default timeout enough? Does detach release the address back to the pool? What does the
  attach error say when the network has no router? Availability zone list
  (`openstackAvailabilityZone` answers 500; try it with `containerId` too).
- Does an instance created without `domain` get its network's domain? (`domain` is Optional+Computed
  in case it does.) Are user `evars` read back unmasked, or masked so `env_vars` would diff?
- Is `expireDays` echoed by GET after create? (The provider doesn't rely on it: it keeps the
  configured days and shows `expireDate`.) Does `PUT /threshold` accept every field as the docs say
  for a customer login, and does an "off" threshold read back with `id` null again?
- Status words besides `provisioning`, `running`, `failed`, `error`, and where the reason lives
  (`statusMessage` or `errorMessage`).

## B-014 Instance workflows and deployments (deferred, owner 2026-10-27)
- **Workflows:** every MTN workflow customers see (6, captured 2026-10-27) is an *operational* one,
  run on an existing instance with inputs, some secret: Create Linux/Windows User, Update Linux User
  Passwd, Install/Uninstall LetsEncrypt SSL, MK8S Cypher Regeneration. None is a provisioning
  workflow the create body's `taskSetId` could take. The owner chose to skip them for now. If they
  come back: a separate resource that runs the workflow on create and again when inputs or
  `triggers` change, with secret inputs write-only.
- **Deployments:** the tenant has none (`/api/deployments` is empty), so `POST /api/instances/{id}/deploys`
  can't be checked against MTN. Deferred until one exists.

## B-015 Prove `mtncloud_project` live (needs permission for one real order)
- Does a new "Create Project" item get an `execution`, and with which statuses? Existing items show
  `execution: null` (D-022). If it stays null, Create always ends at its timeout with a warning:
  then find another completion signal (the order's history, or the pool by name).
- Does the order response carry `order.items[0].id`, as the API reference says?
- `groupName` and `cloud` are sent as IDs (the form's `group` and `cloud` types); the workflow uses
  only the cloud ID. Confirm with the execution's output.

## B-016 Follow-ups from the 2026-10-28 live QA run (dev-docs/qa-2026-10-28.md)
- Kubernetes cluster live: finish scaling, autoscaling, in-place edits and import with a token that
  outlives the build (a Kubernetes build took more than 24 minutes; the token lasts 24). Confirm the
  `worker` block, worker add body and autoscaler body.
- Document in the guide and the cluster page that a cluster apply needs a longer-lived token.
- Done 2026-10-30: a bad group or cloud is titled "Unknown group or cloud", not "Unknown project".
- Done 2026-10-30: the insecure-connection warning shows once (the Framework half gives it for both).
- Done 2026-10-30: legacy resources' and data sources' 403 errors carry the permission hint
  (`internal/legacy/permission_hint.go`, every CRUD wrapped). Found on the way: `mtncloud_virtual_images`
  panicked on any failed list and lost images past the first page; fixed.
- Security groups (owner, 2026-10-30): creating security groups is broken on the platform side (the
  group is made, but its rules aren't applied and it doesn't show in the console). The security group
  and rule pages now say not to rely on them and to ask MTN Cloud support. No code change. The 70+
  minute sync wait is part of the same platform problem.
- Live-untested for the customer role (403): SSL certificate, standalone floating IP, library option
  lists and types, shell script tasks and the workflows built on them, NFS/SMB orders. Need a login
  with those permissions.
- The QA project `tf-acc-proj-w1hl0`: the owner deletes it.
- From the review of the QA fixes (2026-10-28), low severity:
  - Instance resize: if the platform accepts the resize but never applies it, the apply waits out the
    update timeout and fails with a generic timeout. Report the plan and status the instance was
    left at instead (rule 12).
  - Option-source data sources skip options whose value isn't a number, so a skipped option is
    reported as "not available to this login". Say that its value was unreadable.
  - Cypher tfvars import: confirm live that GET returns `data` as a string for the tfvars mount.
    If it returns an object, `value` stays unset after import and the plan replaces the entry.
  - Adding a network to a running instance now replaces it: MTN Cloud reports card IDs as null, so a
    resize can't keep the existing cards. Revisit if a later platform version reports card IDs.

## B-017 App lifecycle: `mtncloud_app` (plan, owner asked 2026-10-28)

Today: the `mtncloud_app` and `mtncloud_apps` data sources read apps, and destroying a managed
service's order deletes its app (D-029). Nothing creates or changes an app. Sources: Context7
`/websites/apidocs_morpheusdata_reference` (POST/PUT/DELETE `/api/apps`, add-instance,
remove-instance, security-groups). There is no app-level power call: power goes per instance
(`mtncloud_instance.power_state`).

| Need | API | Terraform design |
|---|---|---|
| Create an empty app | `POST /api/apps` with `blueprintId: "existing"`, name, group, defaultCloud, environment, labels | `mtncloud_app`: `name`, `group`, `cloud` (names, D-006), `description`, `environment`, `labels` |
| Rename, describe, label, move environment | `PUT /api/apps/{id}` (name, description, labels, environment, ownerId) | in place |
| Add or remove instances | `POST /api/apps/{id}/add-instance` (instanceId, tierName), `/remove-instance` | `instances` set of `{ id, tier }`; a set diff becomes adds and removes, in place |
| Security groups | `PUT /api/apps/{id}/security-groups` (securityGroupIds) | `security_groups` set (names), in place |
| Destroy | `DELETE /api/apps/{id}` with `removeInstances`, `preserveVolumes`, `releaseFloatingIps` | default `removeInstances=off`: the app goes, its instances stay (they are usually `mtncloud_instance` resources of their own); `delete_instances = true` opts in |
| Import | `GET /api/apps/{id}` | by ID |

Open questions before building:
1. Does a sub-tenant login have `POST /api/apps` with `blueprintId: "existing"`? Needs one dry run
   (`apps add -d`) and the role check (customer-permissions.md).
2. Tier names: does add-instance need an existing tier, or create one by name? Read one managed
   service's app (`apps get <id> -j`) once a managed order exists.
3. Ownership: an instance in `instances` that is also a `mtncloud_instance` must not fight it. The
   app resource only manages membership, never the instance's own settings.
4. Managed services' apps come from catalog orders: they are read with the data source, not managed
   by `mtncloud_app` (adopting one would let two resources delete the same VMs).

Tests: unit (set diff to add/remove calls), client contract (add/remove/sg/delete query flags),
provider unit (create → empty plan → add an instance → remove → import), acceptance in `admin`
with two `tf-acc-*` VMs.

## B-018 Remaining work after 2026-10-28 (owner's list, in order)

1. **Live power test** (2026-10-28, VM with a router): stop, start and restart pass, each followed by
   an empty plan. Suspend: the platform accepts the call but the VM stays running; the provider now
   fails it after 2 minutes. Retest suspend once on another image, or document it as unsupported.
2. **Live order destroy**: done for `mtncloud_nginx_web_server` (VM and inventory record deleted). Then, if the owner allows one, a managed order, to confirm the app name and
   node names D-029 assumes (managed apps are matched by the order's name and `<name>-<n>` nodes).
3. **MOS** (D-007, approved in the dev tenant): run the MOS order through Terraform; then design bucket
   management over the S3-compatible API with the tenant's own Cypher keys
   (`secret/MOS-<tenant>_<user>_*`), after checking what PowerScale's S3 supports (versioning,
   lifecycle, bucket policies, ACLs).
4. **App lifecycle**: `mtncloud_app` (B-017), after its two open checks.
5. **Web firewall 403s**: MTN's web firewall answers some calls with an HTML "Access Restricted" page
   and a reference ID (certificate PEMs; NFS/SMB option values such as `'Allow All'`). The client
   should recognise that page and say it is the firewall, with the reference ID, instead of a bare
   403. The owner takes reference `2bE-LH4e-J8xB` to MTN's network team.
6. **QA second pass** (owner granted full permissions): tasks and a job run after creating a task;
   workflows; scale thresholds; power and execute schedules; credentials and key pairs; user roles,
   users and user groups. Left out on purpose: option types and lists, forms, identity sources.
   Data sources: a full filter sweep, each filter argument alone on live data (for example
   `mtncloud_instances` by `image`, `plan`, `tags`, `labels`, `network`), compared with a CLI read
   of the same list. The first run covered every data source unfiltered, 37 filtered reads and 53
   lookups by name, not every filter.
7. Done: a provider test for destroying an imported order that hasn't been applied.
8. The low-severity items in B-016, and B-001 (legacy bugs) as legacy resources migrate.

## B-019 Catalog VM orders send the wrong plan ID (found live 2026-10-28, fixed same day)

`mtncloud_nginx_web_server` (plan `G1S2`, then `G2S4`) was refused by `POST /api/catalog/orders`:
"plan: PLAN value [1969] is not contained within the available options [13103, 6053, 8754, ...]".
Nothing was created. `kPlan` resolves through `/api/instances/service-plans` (the instance wizard's
list), but the order form's `plan` field (option type 2499 on item 41) takes its values from the
option list "Service Plans" (18, `dependsOnCode: resourcepool`), whose IDs differ. A customer can't
read list 18 (`/api/library/option-type-lists/18` → not found), and `/api/options/list?optionTypeId=2499`
answers an empty list without the form's context. Every VM catalog order is affected.
Tried live (refused, nothing created): the plan as an object `{id: 1969}` → the same 400 (the
options are bare numbers); the full object with `code` (as `CreateNginxInstance.sh`'s sample shows)
→ an HTML 403, the web firewall. `/api/options/servicePlans?zoneId=&siteId=&resourcePoolId=` lists
260 plans with G2S4 = 1969 and none of the form's 30 IDs. Group, cloud and project are sent like the
script's sample (`"73"`, `"4"`, `"pool-5"`).
Fixed: the owner captured the console's call (`/options/list?optionTypeListId=18&catalogItemTypeId=41&
resourcepool=pool-5&config.customOptions.resourcepool=pool-5&cloud=4&group-vpn=37...`), which lists
the plans by name with the form's own IDs (G2S4 = 13107). `/api/options/list?optionTypeId=<field>` with
the same context answers the same 30 plans. `kPlan` now resolves through it (`FormChoiceIn`) when the
form's plan field is a select, and falls back to the wizard's list otherwise. Mutation-checked.

## B-020 Floating IP pool check follow-ups (review, 2026-10-28)
- A router whose external network changes in the same apply as a new instance or order with that pool
  fails the plan (the check reads routers as they are). Workaround in the message: change the router
  in its own apply. Revisit if it bites.
- Done 2026-10-30: orders check every pool attribute against the first network of its own group
  (`worker_floating_ip_pool` with `worker_networks`, `master_` with `master_networks`), at plan time
  only, so the warning no longer shows again at create. (Terraform itself may show a plan warning
  again in the apply's own plan.)
- Done 2026-10-30: provider unit tests drive `checkPoolRouter` and the order check, and a client test
  covers a power grace spent on failed reads.

## B-021 Load balancer resources must refresh the per-run snapshot (2026-10-29, from review) — done (D-033)
`LoadBalancerSnapshot` caches one `read` per network and selector for the life of a provider instance.
Data sources only read, so that is safe today. When the load balancer resources land, every write
must drop the cached entries for its network (add a delete-by-prefix to `lookupCache`), or a data
source that `depends_on` the resource reads the load balancer as it was before the change.

## B-022 MOS follow-ups (from the 2026-10-29 review)
- Done 2026-10-30: `timeouts {}` on the bucket settings, `mtncloud_mos_object` and
  `mtncloud_mos_object_acl`. The setting and object ACL models are shared with data sources, which
  have no timeouts block, so those resources read and write the model through the schema without it
  (`storage/timeouts.go`).
- Done 2026-10-30: an upload over an object that already exists keeps its canned ACL, on create too.
  A replace deletes the object with its ACL first, so the object ACL page and example show
  `replace_triggered_by` to set it again in the same apply.
- Done 2026-10-30: metadata name case (ValidateConfig) and retention shortening or removal
  (ModifyPlan) are refused at plan time.
- Done 2026-10-30: a 403 on a bucket name not in the tenant's own list reads "the name is taken by
  another tenant". OneFS keys bucket names per access zone, and MTN puts every tenant's user in zone
  `system`, so all tenants share one namespace (as all AWS accounts do). New `name_prefix` on
  `mtncloud_mos_bucket` (as AWS's `bucket_prefix`): prefix plus 8 random characters, picked again if
  taken. Confirmed live by the owner 2026-10-30: a name another tenant owns answers 403 AccessDenied.
- Re-test object lock and retention live once MTN enables object lock on the cluster (owner: later).

## B-023 Load balancer follow-ups (from the 2026-10-29 review)
- Done 2026-10-29 (plugin f577a90, guide 9.6): piece creates send a `requestId`; the plugin tags the
  piece `mtncloud-req-<id>` and a retry answers `existing: true` with it. Piece creates that time out
  or end with no output are retried with the same ID, and the lookups match the tag first.
- Done 2026-10-29: the plugin's top-level `pending` and `piece` are carried through (`runLBEnv`).
  After a pending answer, ACTIVE counts only once a PENDING_* read was seen, or after
  `LBPendingGrace` (2 min); a piece's ID comes from `piece` first.
- Done 2026-10-29 (plugin f577a90): a listener's description, SNI refs (`[]`) and a member's
  `monitorAddress`/`monitorPort` (null) clear in place. A TERMINATED_HTTPS listener must keep its
  certificate (the plugin refuses null), so the provider refuses one without it at plan time.
- Done 2026-10-29: `mtncloud_load_balancer_l7_policy` (+ `_l7_policies`, `_l7_policy` data sources),
  `mtncloud_load_balancer_instance_member`, `mtncloud_load_balancer_floating_ip`, and the
  `mtncloud_load_balancer_member` data source (D-033).
- Not exposed yet: cluster-group members (`clusterGroup`) and pinning one node of an instance
  (`pinAddress`).

## B-024 Archive follow-ups (from the 2026-10-29 review) — done 2026-10-30
- Client contract tests for `archives.go` (`testdata/archives/`, built from the documented answer
  shape: the only live capture is an empty list), lost-answer adoption, a drift step, and the replace
  test's assertions (old archive gone; refused while it holds files).
- An archive name another tenant uses: a visible one names that tenant (owner compared with
  `whoami`), and a private one, which the list doesn't show, maps the create's "must be unique" to
  "another tenant has an archive named ...".

## B-025 L7 policy positions (live QA t92, 2026-10-29) — provider side done 2026-10-30
- The plugin now inserts and shifts (plugin 07b5e8d, guide 9.6): a position from 1 to count+1 goes
  there and the others move down; past the end goes last; no position keeps an update's place.
  The provider dropped its refusal mapping, and runs one apply's creates and moves on a listener in
  ascending position order (`l7Turn`), so each lands where configured and the next plan is empty.
- Checked live 2026-10-30 (QA t95): a1 b2, then z inserted at 1 (z1 a2 b3: 1 created, 2 moved), then
  b1 z2 a3 in place (3 moved). Each apply landed in the configured order, and the next plan was
  `No changes.`
- The plugin also refuses a change that would leave a REJECT policy with no rules first on the
  listener, including through a shift; the provider passes that message on.
- `_disappears` steps for the L7 policy and the instance member are still missing.

## B-026 Next tasks: apps, resources and groups (owner, 2026-10-30)
Not built yet, to pick up after Kubernetes and clusters:
- **Apps.** No resource creates or changes an app: only the `mtncloud_app` and `mtncloud_apps` data
  sources exist (plan in B-017, with its open questions). Pools for an app's instances come from the
  floating IP options source with `zoneId` + `resourcePoolId` (no VM exists yet), as clusters do.
- **Resources.** An app's instances (add and remove, tiers) and the resources an app holds are part
  of the app slice; nothing manages them today.
- **Groups.** `mtncloud_group` is the legacy resource, never tested against MTN Cloud and not
  migrated. Check what a sub-tenant login may do with `/api/groups` (customer-permissions.md), then
  migrate it to the Framework with the usual tests, or mark it master-only.

## B-027 Catalog order floating IP pools in other projects (2026-10-30)
Pool IDs are per project (api-research, floating IPs): `public-network-02` is `pool-43` in `admin`
and `pool-2002` in the QA project. Orders send the form's fixed value (`admin`'s IDs). Owner,
2026-10-30: this works in every project (MTN's workflow handles it); no change and no catalog QA.
Also confirmed: networks are created before anything is provisioned on them, so an order naming a
network that doesn't exist yet fails at plan time, by design.
