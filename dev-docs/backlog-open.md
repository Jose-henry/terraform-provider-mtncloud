# Open backlog (as of 2026-10-31, after v0.7.0)

Everything still open in [backlog.md](backlog.md). Item numbers (B-NNN, Q-N) point to the full entry there.

## 1. Next, in the owner's order

### 1.1 Kubernetes clusters (B-016)
- [ ] Finish live: scaling, autoscaling, in-place edits and import. A build took more than 24 minutes, which is longer than the 24-hour token lasts, so these need a longer-lived token.
- [ ] Confirm the `worker` block, and the request bodies for adding workers and for the autoscaler.
- [ ] Document in the guide and on the cluster page that a cluster apply needs a longer-lived token.

### 1.2 Apps, resources and groups (B-026, B-017)
- [x] `mtncloud_app` resource: done 2026-10-31. A blank app (name, description, labels, environment, group), members through `mtncloud_app_instance`, destroy keeps the instances, import. Security groups wait for the platform (see B-028).
- [x] Open check: a customer login can make an empty app. Its role has `apps: full` (whoami), and `apps add -b existing -d` shows `POST /api/apps` with `blueprintId`/`templateId` `existing`, `group {id}`, `defaultCloud {id}`, `labels`, `description` (2026-10-31).
- [x] Open check: `add-instance` creates the tier by name. Live 2026-10-31 (QA `t100-app`): a blank app, a VM in new tier `TfAccTier`, plan `No changes.`, destroying the app kept the VM running.
- [x] Ownership: `mtncloud_instance` has no app attribute, so membership (`mtncloud_app_instance`) can't fight it. Managed services' apps take settings through `mtncloud_app_settings`.
- [x] An app's instances (part of the app slice): `mtncloud_app_instance`, 2026-10-31.
- [x] `mtncloud_group`: checked 2026-10-31. The customer role has `admin-groups: read` (whoami), so it reads groups (the data source works) but can't create them. It isn't master-only: a sub-tenant role with full Groups access can. The description says so; migrating it to the Framework waits until a customer needs it.

### 1.3 Day-2 settings on instances and apps something else owns (B-028, approved 2026-10-30)
Catalog orders own what they make and can't change after create, so ordered VMs and apps have no day-2 management today. Owner confirmed: every instance action (resize included) works on an ordered VM, and the workflow doesn't reset changes.

**Order fixes first (catches 1 and 2)**
- [x] **Catch 1: renaming breaks an order's destroy.** Done 2026-10-31: app orders save `app_id` (filled for older or imported state on read) and destroy uses it; VMs were already kept by ID. An order finds its app by the order's name and its VMs by `<name>-<n>` (D-029). A rename through either settings resource means destroy can't find them and refuses (nothing deleted, but stuck).
  - **Fix:** save the app's ID at create, as `instance_ids` already are, and delete by the saved IDs. The name lookup becomes a fallback for older state.
  - **Until it ships:** both settings resources refuse to rename anything an order made.
- [x] **Catch 2: instances added to an order's app die with the order.** Done 2026-10-31, option (a): destroy takes them out of the app (`remove-instance`) and leaves them running. Destroying an order deletes its app with every instance in it, including ones added later and owned by `mtncloud_instance`.
  - **Fix, option (a), approved:** on destroy, delete only the instances the order made (saved `instance_ids`), take any others out of the app first (`remove-instance`), then delete the app.
  - **Rejected, option (b):** refuse at plan to add instances to an order's app.

**`mtncloud_instance_settings`**
- [x] Adopts any existing instance by `instance_id`, whether it came from an order, an app or the console. Destroy only stops tracking it; the owner deletes the VM.
- [x] Day-2, done 2026-10-31: plan resize (disks kept), power state, security groups, description, labels, tags.
- [x] Day-2, added 2026-10-31: data disks (append-only, attached in place), floating IP (`floating_ip_pool`, one per server, re-attached when released), scaling (`scale`), and `expire_date` (read only: expiry can only be set at create). NICs can't be added in place (as on `mtncloud_instance`, where a new network replaces the VM).
- [x] Read-only: image, project, root disk, name and IPs, since changing them would rebuild the VM.
- [x] **Several servers in one instance.** Done 2026-10-31: `public_ips` (one per server, read from each server's container when there are several; a role that can't read them falls back to the instance's one address); drift and re-attach per server. Live check with `scale_factor = 2` still open. Was: (Floating IPs attach per server; `public_ip` still shows one, B-013.) Resize, power, security groups, labels and tags apply to every server. A floating IP is attached per server, but `public_ip` holds only one: expose one public IP per server and check drift on each (B-013).
- [ ] **Research:** changing the number of servers in place. `mtncloud_instance` replaces on `scale_factor`, and an adopted instance can't be replaced.
- [x] **Code:** done by calling `mtncloud_instance`'s own helpers (power, environment, security group names) rather than moving them; its tests pass unchanged. Was: move `mtncloud_instance`'s update steps into shared functions in `internal/services/compute/`. Its tests must pass unchanged first.
- [x] **Tests:** (done 2026-10-31, mutation-checked)
  - adopt, then an empty plan;
  - resize, security groups and power, each followed by an empty plan;
  - destroy leaves the VM;
  - import;
  - VM gone leaves state;
  - drift is fixed;
  - mutation checks.
- [ ] **Live QA (with permission):** one `tf-acc-*` order in `admin`.

**`mtncloud_app_settings`**
- [x] Adopts any existing app by `app_id`, including an order's app. Destroy deletes nothing. Done 2026-10-31 (`mtncloud_app_settings`).
- [x] Description, labels, environment.
- [x] Owner, with a warning: it also changes the owner of every instance in the app.
- [x] Name, once catch 1 is fixed.
- [x] Membership: add and remove instances by tier, as its own resource `mtncloud_app_instance` (2026-10-31), so membership and settings don't fight. Removing a node from a managed service warns that it can break the service.
- [ ] **Security groups: pending.** App-level security groups don't work on MTN yet, so they go through the instance. When they work, only one of the app and the instance settings may own an instance's security groups, or the two undo each other.
- [ ] **State, refresh, prepare-apply:** these work only for Terraform, CloudFormation and ARM blueprint apps. Read one managed service's app type first (`apps get <id> -j`, read-only); leave them out if it doesn't qualify.
- [x] No app-level power, resize, disk, NIC or floating IP call exists: VMs are reconfigured through their instances.

**Order of work:** order fixes, then `mtncloud_instance_settings`, then `mtncloud_app_settings`, then `mtncloud_app` (B-017).

## 2. Features not built

- [x] **Order networks made in the same apply (B-034 #1).** Done 2026-10-31: networks by ID too.
- [x] **Router waits for new subnets (B-034 #2).** Done 2026-10-31: a router made in the same apply as its networks waits for their subnets (within its create timeout) instead of failing with "no subnet yet, try again".
- [ ] **Catalog QA (B-029 #8).** 2026-10-31 later: self-managed Kubernetes passed single-node and two-tier (1 master + 1 worker: `Master` and `Worker` tiers, about 14 minutes; apply, `No changes.`; day-2 `mtncloud_app_settings` plus `mtncloud_instance_settings` with `for_each` on its VMs, `No changes.`; destroy, API 404 checked). Owner chose linked per-VM resources over a wrapped block (2026-10-31: wrapping risks two resources fighting over a VM, and partial failures across VMs). Order every catalog item live (`tf-acc-*` in `admin`): apply, plan `No changes.`, destroy.
  2026-10-31: passed (apply, `No changes.`, destroy) for MySQL, PostgreSQL, MariaDB, MongoDB, Apache, Nginx,
  LAMP, LEMP, WordPress, Managed Database, Bastion, WireGuard, PfSense, Fortinet and SIEM. Blocked on MTN:
  Managed MQ and Streaming (their blueprints 16 and 21 aren't found, B-034 #6), SOPHOS (order 2758 never
  started in an hour). Not yet run: FortiWeb, FortiADC, FortiManager, FortiAnalyzer (BYOL, large disks).
- [x] **Catalog form snapshot in the docs (B-029 #7, report B6).** Done 2026-10-31: `internal/services/catalog/forms/*.json` (written by `TestAccCatalogForms_Snapshot`, drift-checked by `TestAccCatalogForms_MatchSpecs`) feed each argument's description; the `mtncloud_catalog_item` data source shows each input's rules; locked, not-blank, pattern and range are checked at plan.
- [x] **App orders wait for the app (B-034 #3).** Done 2026-10-31: managed services' workflows end once they ask for the app, whose VMs MTN names itself. Create waits for the app named after the order and takes its VMs; destroy deletes the app.
- [x] **Never-blank lists send their first choice (B-034 #4).** Done 2026-10-31: MQ and Streaming workflows failed on `msg_engine`/`kafkaVersion` null.
- [x] **SOPHOS: find the VM its workflow names itself (B-034 #10).** Done 2026-10-31: the VM the workflow reports in its output (its console link, else " Instance name : "), only within the order's project; 3-hour create timeout; destroy warns that the Heat stack stays. `name` is optional and unused.
- [ ] **Ask MTN: Managed MQ and Streaming blueprints 16 and 21 not found (B-034 #6).**
- [ ] **Ask MTN: remove the SOPHOS Heat stack left by QA order 2758** (`<initials>-72-MTN-CLOUD-SPH001-1-stack`). Its floating IP keeps router `tf-acc-rtr-cat-04jkx` (1271) from deleting, so it and networks `tf-acc-net-cat-{a,b}-04jkx` (690, 691) are still up; the VM (4639) is deleted. Then `terraform destroy` in the QA folder `t97-catalog`.
- [x] **A failed order keeps the VM it made (SOPHOS QA order 2759).** Done 2026-10-31: MTN's SOPHOS workflow failed at "Convert to Managed" with the VM up; create and refresh now keep the order (tainted, `instance_ids` saved), so the next apply deletes the VM and orders again, and destroy deletes it.
- [ ] **Delete QA's SOPHOS VM 4641** (`<initials>-72-MTN-CLOUD-SPH001-2`, from failed QA order 2759, before the fix above). Deleting it was refused by the session's permission check: the owner deletes it, or allows the delete.
- [x] **Review of the B-017/B-023/B-028 batch (2026-10-31).** All 8 findings fixed with tests: imported app order vs added members, settings restart on adopt, failed resize, unconfirmed floating IP, partial-failure state, app default group, unknowns on error paths, clearing an app's environment; `DeleteAppOnly` keeps floating IPs.
- [ ] **Decide: leftover `tf-acc-vpn-uyi6` (4572) and `tf-acc-win-8aho` (4600)** from earlier sessions are still running on networks 688/689; not in this session's ledger.
- [x] **Failed order errors carry the workflow's reason (B-034 #5).** Done 2026-10-31: last lines of the failed task's output, secret lines dropped.
- [x] **Legacy descriptions (report B5).** Done 2026-10-31: all 80 say what the type is for; data sources say what they look up by (from their schema).
- [x] **MOS order resource (B-032).** Done 2026-10-31 as `mtncloud_mos_access` (owner: adopt when keys exist, order only when none or `rotate = true`, rotate on a `rotate_trigger` change). Live QA open.
- [ ] **Cluster layouts by code (B-033).** With the cluster work.

- [x] **Firewall 403s (B-018 #5, report A6).** Done 2026-10-31: the firewall page, a network block and a role refusal each get their own message. Confirm the match on the next real firewall page.
- [x] **Customer-login refusal at plan time (B-007).** Closed 2026-10-31: D-024 removed every master-only type, so nothing is left to refuse. Role-dependent types (`mtncloud_group`) say so in their description, and a 403 names the role.
- [x] **`dhcp_range` and `host_routes` on `mtncloud_network` (B-010).** Done 2026-10-31: formats proved live on a QA network ([api-research](api-research.md#network-dhcp-range-and-host-routes-b-010)); new `status` attribute, and a failed network plans a resend.
- [x] **Load balancer members (B-023): pinning one node.** Done 2026-10-31: `pin_address` on `mtncloud_load_balancer_instance_member` (the plugin's `pinAddress`; import `.../<instance_id>@<address>`).
- [ ] **Cluster-group members (`clusterGroup`, B-023).** With the cluster work.
- [ ] **Instance workflows and deployments (B-014).** Deferred by the owner.
- [x] **Catalog item inputs (B-008).** Already done with the orders: the `mtncloud_catalog_item` data source shows each input (name, type, required, default, choices, and the form's rules).

## 3. Live checks still open (each needs permission, or MTN)

- [ ] **MTN Network (B-011).** Answered live 2026-10-31 (t102): a `PUT` with an empty description clears it (the API then returns none), and the plan after is `No changes.` Still open: does an empty IPv6 DNS clear it? IPv6 defaults. The status words of a failed network. A gateway change while a router port holds the old address. The status code of an "in use" security group refusal.
- [ ] **MTN Router (B-012).**
  - Is the GET current straight after a PUT? **Yes** (live 2026-10-31, t102: a description update, then `No changes.`).
  - Does re-sending `externalNetwork` keep the IP? Not re-sent: an update that keeps the gateway leaves `externalNetwork` out, and the external IP stayed the same (checked live by hash, t102). Re-sending it is still unchecked.
  - When is the gateway IP set?
  - Which status words besides `provisioning` and `ok`?
  - Does an `external_network` in another case give a no-op update after import?
- [ ] **Instance (B-013).**
  - Do the name and hostname policies keep, refuse or ignore what's sent?
  - Resize bodies.
  - Do empty labels or tags clear?
  - Delete policies.
  - Floating IP:
    - with `scale_factor` above 1: **works** (live 2026-10-31, t102): one address per server, two distinct, `public_ips` lists both, `No changes.` after;
    - how long until it shows: create with both attached took 5 minutes in all; a detach of both showed in 8 seconds;
    - is the address released on detach: detached from both servers (each server's container shows none); whether it goes back to the pool is still unchecked;
    - the error when the network has no router.
  - The availability zone list.
  - The default `domain`.
  - Masking of `evars`.
  - The `expireDays` echo.
  - The threshold `PUT`.
  - Status words.
- [ ] **Project order (B-015).** Needs one real order. Does it get an `execution`? Does the response carry `order.items[0].id`? Group and cloud are sent as IDs.
- [ ] **Power suspend (B-018 #1).** The VM stayed running. Retest on another image, or document suspend as unsupported.
- [ ] **Managed order destroy (B-018 #2).** Confirm the app and node names that D-029 assumes.
- [ ] **QA second pass (B-018 #6).**
  - Tasks and a job run.
  - Workflows.
  - Scale thresholds.
  - Power and execute schedules.
  - Credentials and key pairs.
  - User roles, users and user groups.
- [ ] **Data source filter sweep (B-018 #6).** Each filter argument on its own against live data, compared with a CLI read.
- [ ] **Customer-role 403s (B-016).** Not tested yet: SSL certificate, standalone floating IP, option lists and types, shell script tasks and their workflows, NFS/SMB orders. Needs a login with those permissions.
- [ ] **Cypher tfvars import (B-016).** Confirm that GET returns `data` as a string.
- [ ] **MOS object lock and retention (B-022).** Waiting for MTN to enable object lock.
- [ ] **Security groups (B-009).** Blocked on the platform bug, where rules aren't applied. Once fixed: egress with no CIDR, group-source, `any` and `icmp` rules, clearing the description, `remove_default_egress`, groups with several locations.
- [ ] **Sub-tenant login (B-006).** Confirm the single-backslash login with a real sub-tenant run.

## 4. Small code follow-ups

- [x] **Resize that never applies (B-016).** Done 2026-10-31: the error names the plan and disks the instance is still on.
- [x] **Unreadable option values (B-016).** Done 2026-10-31.
- [x] **Missing `_disappears` tests.** Done 2026-10-31: L7 policy, load balancer instance member, and security group rule (with drift and a refused rule partway through a group update).
- [ ] **Revisit if it bites.**
  - Adding a network to a running instance replaces it (B-016).
  - A router change in the same apply as a new pool user fails the plan (B-020).

## 5. Debt and housekeeping

- [x] **Legacy bugs (B-001).** Done 2026-10-31: no dropped `json.Unmarshal` error is left; the logo uploads went with their types (D-025).
- [ ] **Legacy lint debt (B-002).** The `.golangci.yml` exclusions for `internal/legacy/` only shrink.
- [x] **Example defects (B-003).** Done 2026-10-31: the known-defects list is empty.
- [x] **Local tfvars (B-004).** Done 2026-10-31. Remove the stale `username`, `password` and `tenant_subdomain` keys from the local quickstart `terraform.tfvars`.
- [ ] **Open questions.**
  - Q6: a dedicated test tenant and credentials for acceptance CI.
- [x] **Stale entries in backlog.md.** Done 2026-10-31: build order, Q1, Q3 and Q4 updated.
  - The "Build order" list strikes through only steps 1 and 2, though security groups, networks, routers, projects, orders, MOS and load balancers are built.
  - Q3 (load balancer API) was answered by D-031.
  - Q4's payloads may be stale.
