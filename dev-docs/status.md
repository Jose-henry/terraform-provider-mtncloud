# Status: what's done, what's next

Last updated 2026-10-26 (scaffold commit). Update this file at the end of every PR.

## Done

### Fork and rename
- Upstream provider copied into `internal/legacy` (package `legacy`). `main.go` serves `legacy.Provider()`.
- Module `github.com/mtn/terraform-provider-mtncloud`; types `mtncloud_*`; env vars `MTNCLOUD_API_*`;
  docs and examples say "MTN Cloud". The external SDK is imported as `sdk`. One API wire value is
  allow-listed (pool type code in `resource_ipv4_ip_pool.go`).
- Identifiers that broke under the rename were fixed (`PlanPayload`, `PriceSetPayload`, `PricePayload`,
  `globals.go`).

### Scope (D-002)
- Removed 13 resources and 2 data sources (non-OpenStack clouds and instances, ARM/CloudFormation, vSphere data sources),
  with their docs, templates and examples. Kept: K8s/Helm/Terraform blueprints, all integrations,
  all appliance config.
- What's left: 112 resources and 63 data sources, all documented.

### Quality gates (all passing locally)
| Gate | Result |
|---|---|
| `go build`, `go vet` | ok |
| `go test` | 6 schema tests pass (InternalValidate, `mtncloud_` prefix, removed types stay removed, no upstream name in schema, env vars, secrets Sensitive) |
| `golangci-lint` v2 | 0 issues (legacy has a recorded debt baseline, B-002) |
| `golangci-lint fmt` | clean |
| tfplugindocs generate + validate | ok, deterministic, no drift |
| `scripts/check-names.sh` | ok (mutation-tested) |
| `scripts/validate-examples.sh` | 313 files on Terraform 1.17.0, 1.5.7 and OpenTofu 1.13.2; 22 known upstream defects tracked (B-003); mutation-tested |
| `terraform fmt -check` on examples | clean |
| `govulncheck` | no vulnerabilities (grpc 1.84.0, x/net 0.61.0, x/text 0.43.0, toolchain go1.26.10) |
| `goreleaser check` | valid (`archives.formats`) |

### Repo structure (mirrors the Onesi repo)
- `CLAUDE.md` (lean index), `.claude/rules/` (workflow, terraform-provider, testing, isolation,
  graphify, tools), `.claude/agents/mtncloud-code-reviewer.md`, `.claude/skills/ship-check/`,
  `.claude/hooks/graphify-update.mjs` + `settings.json` hooks.
- `.github/workflows/`: `ci.yml` (lint, test -race, docs drift, name gate, examples on 3 CLIs,
  goreleaser check, govulncheck; SHA-pinned actions), `acceptance.yml` (manual only),
  `release.yml` (goreleaser + GPG). Dependabot monthly. PR template.
- `internal/` package layout with a README contract per package (no code yet).
- `dev-docs/` (this folder), `Makefile` (`make ci` = every PR gate), `README.md`, `CHANGELOG.md`,
  `.gitignore` (tfvars, state, .terraform, env, graphify-out), `.gitattributes` (LF),
  `.editorconfig`, `.graphifyignore` (Go only).
- `examples/quickstart` rewritten (no real IDs). The local `terraform.tfvars` is git-ignored.

### Research captured
- MTN Drive docs: project, network and router workflows, instance access ([prerequisites.md](prerequisites.md)).
- Context7 API reference: networks, routers, security groups (+rules, locations), instances, floating
  IPs ([api-research.md](api-research.md)).
- CLI payloads: catalog item types (39), instance types (114, 45 public OpenStack), resource pools
  ([mtn-cloud-domain.md](mtn-cloud-domain.md)).
- MOS automation scripts: key creation, Cypher layout, rotation hazard ([mtn-cloud-domain.md](mtn-cloud-domain.md#mos-mtn-object-storage)).

## Next (in order, each one a PR with tests, after the owner approves its plan)

1. **Answer the open questions** Q1–Q6 and capture payloads P1–P10 ([backlog.md](backlog.md),
   [api-research.md](api-research.md), [prerequisites.md](prerequisites.md)).
2. **Plugin Framework provider + mux** (D-005): an empty Framework provider served next to legacy;
   the provider config gains `cloud`, `group` and a master-only `tenant` override.
3. **`internal/client`**: retries, rate limit, ctx, redaction, typed errors, lookup cache; httptest
   contract tests.
4. **`internal/tenancy`**: whoami, master gating (D-004), with the master-only list from Q1.
5. **Project resolution** ([prerequisites.md](prerequisites.md)): `mtncloud_project` data source +
   `mtncloud_projects`.
6. **Catalog data sources** ([catalog-lookups.md](catalog-lookups.md)): images, plans, catalog items.
7. `mtncloud_security_group` (+ rule) → `mtncloud_network` (MTN Network) → `mtncloud_router`
   (MTN Router) → `mtncloud_instance` (OpenStack, floating IP) → `mtncloud_project` resource →
   `mtncloud_catalog_order` → `mtncloud_object_storage` (MOS) → app, load balancer, SSL certificate.
8. Migrate legacy resources that customers use most into the Framework, fixing B-001 bugs on the way.
9. First release: rename the GitHub repo for the registry (Q5), GPG key, `v0.1.0`.
