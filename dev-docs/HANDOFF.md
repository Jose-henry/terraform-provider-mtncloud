# Handoff prompt

Paste the block below into a new Claude Code session opened at the repo root.

---

You are continuing work on **terraform-provider-mtncloud**, the Terraform provider for MTN Cloud
(`https://console.cloud.mtn.ng`, HPE Morpheus on OpenStack). It is forked from an MIT upstream
provider and renamed throughout.

**Read first, in this order:**
1. `CLAUDE.md`: the index, identity, domain summary, and architecture.
2. `.claude/rules/workflow.md`: how we work. Key rules:
   - **Always read the docs**: MTN's Drive docs (folder `V0.1_Documentations`) and the platform API
     through Context7 `/websites/apidocs_morpheusdata_reference`. Never design from memory.
   - **Plan before code.** Propose files, schema, API source and tests, then wait for the owner's
     approval before writing Go.
   - The rule file says branch → PR → merge; the owner currently has work pushed straight to `main`.
     Every slice still runs all gates, the `mtncloud-code-reviewer` agent and mutation checks, and a
     push waits for that slice's live QA. Never add a Co-Authored-By line.
   - Never run acceptance tests or any write call against MTN Cloud without explicit permission.
     Never print or commit secrets, tfvars, state, or real tenant IDs/IPs.
3. `.claude/rules/terraform-provider.md`, `testing.md`, `isolation.md`: the engineering bar. After
   `apply`, `plan` must say `No changes.`
4. `dev-docs/status.md`: what's done and what's next, then `backlog-open.md` (the open list). Then `backlog.md` (open questions Q1–Q6),
   `prerequisites.md` (project name → pool ID resolution, synced default security group),
   `catalog-lookups.md` (names, not IDs), `api-research.md` (payloads P1–P10 still needed),
   `mtn-cloud-domain.md` (catalog items, images, MOS), `decisions.md` (D-001…D-043), and
   `catalog-forms.md` (each order form's inputs).

**Where things stand (2026-11-04):** v0.10.1 is released (registry `jose-henry/mtncloud`); 124
resources and 108 data sources, all for customer logins. It carries the load balancer plugin updates up
to 6c47249 (see `CHANGELOG.md`). Live QA runs in the dev test tenant: `tf-acc-*`
objects in project `admin`, each logged, deleted and checked with an API 404.

- **Clusters** (`mtncloud_managed_kubernetes_cluster`, `mtncloud_docker_cluster`, D-043): built and
  changed in place only when healthy (cluster `ok`, every node `provisioned`); a failed or timed-out
  create fails the apply and is replaced on the next one. Plan-time checks on a change only warn,
  because `terraform destroy` plans the configuration first; the apply checks again on a fresh read.
  Destroy force-deletes (`removeResources`, `removeInstances`, `force`) in any state. Docker is proven
  live; Kubernetes workers fail to join on MTN's side, so a healthy Kubernetes build is unproven.
- **Load balancers** run through the console task `lboperate.terraform` (D-031); the plugin's own
  guide is `../Octavia-Load-Balancer-Plugin/docs/TERRAFORM-AGENT-GUIDE.md`. Read it before changing
  anything there; the plugin's owner sends updates as short notes.

**Domain facts you must not get wrong:**
- Customers type a **project name**. Its resource pool is named `<project>-<tenant account name>`.
  Resolve it to the pool ID inside the caller's tenant (filter `tenants[]` by tenant ID). Report the
  short name back.
- A new project's order takes about 10 minutes; its default security group and floating IP pools
  then sync in, which can take up to an hour. Nothing waits for that sync (owner, D-022): lookups that
  find nothing say to wait up to an hour (`client.SyncHint`).
- Networking is **MTN Network** / **MTN Router** (Neutron). Network first, then router (external
  network optional, internal networks).
- Object storage is **MOS**. Keys come from catalog item 13 (`s32311`), are stored in Cypher, and
  **ordering keys rotates the tenant-wide key**. Customers never see "S3".
- Users pick images, plans, projects and catalog items by **name or code**; IDs are resolved at plan
  time. No numeric IDs in Go.

- Customer roles get **403** on admin endpoints (`/api/zones`, `/api/service-plans`, floating IPs).
  Use the endpoints the console's wizard calls (D-016), and confirm every new endpoint with a
  sub-tenant login.
- The platform CLI runs only through `scripts/cli/capture.ps1`: reads, and `-d` dry runs for
  anything that writes.

**Your first task:** read the files above, then take the next open item in `backlog-open.md`
(Kubernetes clusters once MTN fixes the worker join, or the next item the owner names). Propose its plan
(schema with every attribute's Required/Optional/Computed and why, client methods with their Context7
source, tests, docs) and wait for approval before writing Go. Use the `ship-check` skill before
calling anything done, and the `mtncloud-code-reviewer` agent on every diff.

- A VM with the agent on (the default) stays "provisioning" until its agent reports in: its network
  needs an MTN Router with an external network.
- The API token from the CLI lasts 24 minutes; long creates (Kubernetes) outlive it.
- MTN's web firewall blocks some bodies (certificate PEMs, quoted option values) with an HTML 403.

**Local tooling notes (Windows):** `-race` needs cgo, so it runs in CI. `make` may be missing, so run
the commands in the Makefile directly. `scripts/validate-examples.sh` needs `PYTHON=python` and
`TERRAFORM=<path to terraform>`.

---
