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
     Ask which applies before your first commit.
   - Never run acceptance tests or any write call against MTN Cloud without explicit permission.
     Never print or commit secrets, tfvars, state, or real tenant IDs/IPs.
3. `.claude/rules/terraform-provider.md`, `testing.md`, `isolation.md`: the engineering bar. After
   `apply`, `plan` must say `No changes.`
4. `dev-docs/status.md`: what's done and what's next. Then `backlog.md` (open questions Q1–Q6),
   `prerequisites.md` (project name → pool ID resolution, synced default security group),
   `catalog-lookups.md` (names, not IDs), `api-research.md` (payloads P1–P10 still needed),
   `mtn-cloud-domain.md` (catalog items, images, MOS), `decisions.md` (D-001…D-029), and
   `qa-2026-10-28.md` (the live QA run).

**Where things stand:** v0.4.0 is tagged; `main` is ahead with instance power control, catalog
order destroy (D-029) and the `mtncloud_instances` network filter. 89 resources and 86 data sources,
all for customer logins. Live QA ran on 2026-10-28 in the dev test tenant. The remaining work, in
order, is `dev-docs/backlog.md` B-018 (live power and order-destroy tests, MOS, `mtncloud_app`, the
web firewall error, a QA pass of automation and users).

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

**Your first task:** read the files above, then take the next open item in B-018. Propose its plan
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
