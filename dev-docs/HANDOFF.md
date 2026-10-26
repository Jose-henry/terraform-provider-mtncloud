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
   - Branch → PR → green CI → merge. Never commit to `main`.
   - Never run acceptance tests or any write call against MTN Cloud without explicit permission.
     Never print or commit secrets, tfvars, state, or real tenant IDs/IPs.
3. `.claude/rules/terraform-provider.md`, `testing.md`, `isolation.md`: the engineering bar. After
   `apply`, `plan` must say `No changes.`
4. `dev-docs/status.md`: what's done and what's next. Then `backlog.md` (open questions Q1–Q6),
   `prerequisites.md` (project name → pool ID resolution, synced default security group),
   `catalog-lookups.md` (names, not IDs), `api-research.md` (payloads P1–P10 still needed),
   `mtn-cloud-domain.md` (catalog items, images, MOS), and `decisions.md` (D-001…D-009).

**Where things stand:** the scaffold is on `main`. The provider builds, 6 schema tests pass, lint is
clean, docs are generated, and CI is defined. No MTN-specific resource code exists yet. All new
resources will use the Plugin Framework, muxed with `internal/legacy` (SDKv2).

**Domain facts you must not get wrong:**
- Customers type a **project name**. Its resource pool is named `<project>-<tenant account name>`.
  Resolve it to the pool ID inside the caller's tenant (filter `tenants[]` by tenant ID). Report the
  short name back.
- A new project takes about 10 minutes and syncs in a default security group and pools. Dependents
  must wait for that sync.
- Networking is **MTN Network** / **MTN Router** (Neutron). Network first, then router (external
  network optional, internal networks).
- Object storage is **MOS**. Keys come from catalog item 13 (`s32311`), are stored in Cypher, and
  **ordering keys rotates the tenant-wide key**. Customers never see "S3".
- Users pick images, plans, projects and catalog items by **name or code**; IDs are resolved at plan
  time. No numeric IDs in Go.

**Your first task:** read the files above, then ask the owner which open questions (Q1–Q6) and
payloads (P1–P10) they can answer now. Propose the plan for the next step in `status.md`
("Plugin Framework provider + mux") and wait for approval. Use the `ship-check` skill before
calling anything done, and the `mtncloud-code-reviewer` agent on every diff.

**Local tooling notes (Windows):** `-race` needs cgo, so it runs in CI. `make` may be missing, so run
the commands in the Makefile directly. `scripts/validate-examples.sh` needs `PYTHON=python` and
`TERRAFORM=<path to terraform>`.

---
