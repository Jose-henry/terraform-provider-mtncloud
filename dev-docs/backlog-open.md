# Open backlog (as of 2026-10-30, after v0.7.0)

Everything still open in [backlog.md](backlog.md). Item numbers (B-NNN, Q-N) point to the full entry there.

## 1. Next, in the owner's order

### 1.1 Kubernetes clusters (B-016)
- [ ] Finish live: scaling, autoscaling, in-place edits and import. A build took more than 24 minutes, which is longer than the 24-hour token lasts, so these need a longer-lived token.
- [ ] Confirm the `worker` block, and the request bodies for adding workers and for the autoscaler.
- [ ] Document in the guide and on the cluster page that a cluster apply needs a longer-lived token.

### 1.2 Apps, resources and groups (B-026, B-017)
- [ ] `mtncloud_app` resource: create an empty app, rename and label it, add and remove instances (tiers), security groups, destroy, import. Today there are only the data sources.
- [ ] Open check: can a customer login create an empty app (`POST /api/apps` with `blueprintId: "existing"`)? Needs `apps add -d` and a role check.
- [ ] Open check: does add-instance need an existing tier, or create one by name? Read a managed service's app.
- [ ] Ownership: app membership must not fight `mtncloud_instance`. Managed services' apps are read with the data source only.
- [ ] An app's instances and resources (part of the app slice).
- [ ] `mtncloud_group`: check what a sub-tenant login may do with `/api/groups`, then migrate it to the Framework with the usual tests, or mark it master-only.

### 1.3 Day-2 settings on instances and apps something else owns (B-028, approved 2026-10-30)
Catalog orders own what they make and can't change after create, so ordered VMs and apps have no day-2 management today. Owner confirmed: every instance action (resize included) works on an ordered VM, and the workflow doesn't reset changes.

**Order fixes first (catches 1 and 2)**
- [ ] **Catch 1: renaming breaks an order's destroy.** An order finds its app by the order's name and its VMs by `<name>-<n>` (D-029). A rename through either settings resource means destroy can't find them and refuses (nothing deleted, but stuck).
  - **Fix:** save the app's ID at create, as `instance_ids` already are, and delete by the saved IDs. The name lookup becomes a fallback for older state.
  - **Until it ships:** both settings resources refuse to rename anything an order made.
- [ ] **Catch 2: instances added to an order's app die with the order.** Destroying an order deletes its app with every instance in it, including ones added later and owned by `mtncloud_instance`.
  - **Fix, option (a), approved:** on destroy, delete only the instances the order made (saved `instance_ids`), take any others out of the app first (`remove-instance`), then delete the app.
  - **Rejected, option (b):** refuse at plan to add instances to an order's app.

**`mtncloud_instance_settings`**
- [ ] Adopts any existing instance by `instance_id`, whether it came from an order, an app or the console. Destroy only stops tracking it; the owner deletes the VM.
- [ ] Day-2, same as `mtncloud_instance`: plan resize, extra disks and NICs, power state, security groups, floating IP, labels, tags, expiry, scale thresholds.
- [ ] Read-only: image, project, root disk, name and IPs, since changing them would rebuild the VM.
- [ ] **Several servers in one instance.** Resize, power, security groups, labels and tags apply to every server. A floating IP is attached per server, but `public_ip` holds only one: expose one public IP per server and check drift on each (B-013).
- [ ] **Research:** changing the number of servers in place. `mtncloud_instance` replaces on `scale_factor`, and an adopted instance can't be replaced.
- [ ] **Code:** move `mtncloud_instance`'s update steps into shared functions in `internal/services/compute/`. Its tests must pass unchanged first.
- [ ] **Tests:**
  - adopt, then an empty plan;
  - resize, security groups and power, each followed by an empty plan;
  - destroy leaves the VM;
  - import;
  - VM gone leaves state;
  - drift is fixed;
  - mutation checks.
- [ ] **Live QA (with permission):** one `tf-acc-*` order in `admin`.

**`mtncloud_app_settings`**
- [ ] Adopts any existing app by `app_id`, including an order's app. Destroy deletes nothing.
- [ ] Description, labels, environment.
- [ ] Owner, with a warning: it also changes the owner of every instance in the app.
- [ ] Name, once catch 1 is fixed.
- [ ] Membership: add and remove instances by tier. Removing a node from a managed service warns that it can break the service.
- [ ] **Security groups: pending.** App-level security groups don't work on MTN yet, so they go through the instance. When they work, only one of the app and the instance settings may own an instance's security groups, or the two undo each other.
- [ ] **State, refresh, prepare-apply:** these work only for Terraform, CloudFormation and ARM blueprint apps. Read one managed service's app type first (`apps get <id> -j`, read-only); leave them out if it doesn't qualify.
- [ ] No app-level power, resize, disk, NIC or floating IP call exists: VMs are reconfigured through their instances.

**Order of work:** order fixes, then `mtncloud_instance_settings`, then `mtncloud_app_settings`, then `mtncloud_app` (B-017).

## 2. Features not built

- [x] **Order networks made in the same apply (B-034 #1).** Done 2026-10-31: networks by ID too.
- [x] **Router waits for new subnets (B-034 #2).** Done 2026-10-31: a router made in the same apply as its networks waits for their subnets (within its create timeout) instead of failing with "no subnet yet, try again".
- [ ] **Catalog QA (B-029 #8).** Order every catalog item live (`tf-acc-*` in `admin`): apply, plan `No changes.`, destroy.
- [x] **Catalog form snapshot in the docs (B-029 #7, report B6).** Done 2026-10-31: `internal/services/catalog/forms/*.json` (written by `TestAccCatalogForms_Snapshot`, drift-checked by `TestAccCatalogForms_MatchSpecs`) feed each argument's description; the `mtncloud_catalog_item` data source shows each input's rules; locked, not-blank, pattern and range are checked at plan.
- [x] **App orders wait for the app (B-034 #3).** Done 2026-10-31: managed services' workflows end once they ask for the app, whose VMs MTN names itself. Create waits for the app named after the order and takes its VMs; destroy deletes the app.
- [x] **Never-blank lists send their first choice (B-034 #4).** Done 2026-10-31: MQ and Streaming workflows failed on `msg_engine`/`kafkaVersion` null.
- [x] **Failed order errors carry the workflow's reason (B-034 #5).** Done 2026-10-31: last lines of the failed task's output, secret lines dropped.
- [x] **Legacy descriptions (report B5).** Done 2026-10-31: all 80 say what the type is for; data sources say what they look up by (from their schema).
- [ ] **MOS order resource (B-032).** Proposal; needs the owner's approval.
- [ ] **Cluster layouts by code (B-033).** With the cluster work.

- [x] **Firewall 403s (B-018 #5, report A6).** Done 2026-10-31: the firewall page, a network block and a role refusal each get their own message. Confirm the match on the next real firewall page.
- [ ] **Customer-login refusal at plan time (B-007).** Refuse master-only resources at plan for a customer login (`internal/tenancy`, D-004), before the API's 403.
- [ ] **`dhcp_range` and `host_routes` on `mtncloud_network` (B-010).** Blocked until a real value is created in the console and captured.
- [ ] **Load balancer members (B-023).** Cluster-group members (`clusterGroup`) and pinning one node of an instance (`pinAddress`).
- [ ] **Instance workflows and deployments (B-014).** Deferred by the owner.
- [ ] **Catalog item inputs (B-008).** Show each item's inputs (name, type, required, default, choices) on `mtncloud_catalog_items`. First check whether this was done when orders were built.

## 3. Live checks still open (each needs permission, or MTN)

- [ ] **MTN Network (B-011).** Does a `PUT` with an empty description or IPv6 DNS clear it? IPv6 defaults. The status words of a failed network. A gateway change while a router port holds the old address. The status code of an "in use" security group refusal.
- [ ] **MTN Router (B-012).**
  - Is the GET current straight after a PUT?
  - Does re-sending `externalNetwork` keep the IP?
  - When is the gateway IP set?
  - Which status words besides `provisioning` and `ok`?
  - Does an `external_network` in another case give a no-op update after import?
- [ ] **Instance (B-013).**
  - Do the name and hostname policies keep, refuse or ignore what's sent?
  - Resize bodies.
  - Do empty labels or tags clear?
  - Delete policies.
  - Floating IP:
    - with `scale_factor` above 1;
    - how long until it shows;
    - is the address released on detach;
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

- [ ] **Resize that never applies (B-016).** Report the plan and status the instance was left at, instead of a generic timeout.
- [ ] **Unreadable option values (B-016).** Option-source data sources should say an option's value was unreadable, not "not available to this login".
- [ ] **Missing `_disappears` tests.**
  - L7 policy and load balancer instance member (B-025).
  - Security group rule: also missing its drift step and a failure partway through Update (B-009).
- [ ] **Revisit if it bites.**
  - Adding a network to a running instance replaces it (B-016).
  - A router change in the same apply as a new pool user fails the plan (B-020).

## 5. Debt and housekeeping

- [ ] **Legacy bugs (B-001).** 32 `json.Unmarshal` errors are dropped, so a bad response plans a recreate. Two logo uploads fail silently. Fix each as its resource migrates.
- [ ] **Legacy lint debt (B-002).** The `.golangci.yml` exclusions for `internal/legacy/` only shrink.
- [ ] **Example defects (B-003).** 22 example files use arguments the schema doesn't have (`scripts/examples-known-defects.txt`).
- [ ] **Local tfvars (B-004).** Remove the stale `username`, `password` and `tenant_subdomain` keys from the local quickstart `terraform.tfvars`.
- [ ] **Open questions.**
  - Q1: the authoritative list of master-only features.
  - Q6: a dedicated test tenant and credentials for acceptance CI.
- [ ] **Stale entries in backlog.md.**
  - The "Build order" list strikes through only steps 1 and 2, though security groups, networks, routers, projects, orders, MOS and load balancers are built.
  - Q3 (load balancer API) was answered by D-031.
  - Q4's payloads may be stale.
