# CLAUDE.md — terraform-provider-mtncloud

Terraform provider for **MTN Cloud** (`https://console.cloud.mtn.ng`), MTN Nigeria's cloud platform.
MTN Cloud runs HPE Morpheus on **OpenStack**. This provider is forked from the MIT-licensed upstream
`gomorpheus/terraform-provider-morpheus` (local copy: `../terraform-provider-morpheus`), stripped to
what MTN Cloud offers and rebuilt to MTN's standards.

**Quality bar:** run `terraform apply`, then `terraform plan` straight away. It must print
`No changes.` for every resource, every time. A resource that can't do this isn't finished.

## Where things are

| Need | Read |
|---|---|
| **Rule: always read MTN's Drive docs + Context7 API docs before designing** | [.claude/rules/workflow.md](.claude/rules/workflow.md) |
| What's done, what's next | [dev-docs/status.md](dev-docs/status.md) |
| Handoff prompt for a new session | [dev-docs/HANDOFF.md](dev-docs/HANDOFF.md) |
| Prerequisites: project name → pool ID, synced default SG | [dev-docs/prerequisites.md](dev-docs/prerequisites.md) |
| How to work here (plan first, branches, PRs, secrets) | [.claude/rules/workflow.md](.claude/rules/workflow.md) |
| Provider engineering rules (schema, diffs, retries, UX) | [.claude/rules/terraform-provider.md](.claude/rules/terraform-provider.md) |
| Test layers, acceptance tests, CI gates | [.claude/rules/testing.md](.claude/rules/testing.md) |
| Isolation (tenants, tests, code, credentials) | [.claude/rules/isolation.md](.claude/rules/isolation.md) |
| Code graph (graphify) | [.claude/rules/graphify.md](.claude/rules/graphify.md) |
| Docs lookup (Context7), CLI payloads, tools | [.claude/rules/tools.md](.claude/rules/tools.md) |
| MTN Cloud facts: catalog items, images, MOS, names | [dev-docs/mtn-cloud-domain.md](dev-docs/mtn-cloud-domain.md) |
| API shapes per resource, payloads still needed | [dev-docs/api-research.md](dev-docs/api-research.md) |
| Names-not-IDs design (catalog data sources) | [dev-docs/catalog-lookups.md](dev-docs/catalog-lookups.md) |
| What a customer (sub-tenant) login can read | [dev-docs/customer-permissions.md](dev-docs/customer-permissions.md) |
| Decisions (D-NNN) | [dev-docs/decisions.md](dev-docs/decisions.md) |
| Open questions, build order, known debt | [dev-docs/backlog.md](dev-docs/backlog.md) |

`docs/` is **generated registry documentation** (tfplugindocs, from `templates/` and `examples/`).
Never hand-edit it. Engineering notes go in `dev-docs/`.

## Identity

| Item | Value |
|---|---|
| Provider / type prefix | `mtncloud` / `mtncloud_*` |
| Registry source | `mtn/mtncloud` |
| Go module | `github.com/mtn/terraform-provider-mtncloud` |
| Env vars | `MTNCLOUD_API_URL`, `MTNCLOUD_API_TOKEN`, `MTNCLOUD_API_USERNAME`, `MTNCLOUD_API_PASSWORD`, `MTNCLOUD_API_TENANT`, `MTNCLOUD_API_SECURE` |
| Cloud | OpenStack only (for example `MTNNG_CLOUD_AZ_1`) |

**Naming rule:** the upstream product name must not appear in code, generated docs, examples,
schema descriptions or errors. The exceptions are: the LICENSE notice; the external SDK import
`github.com/gomorpheus/morpheus-go-sdk`, always aliased `sdk` (target: imported only in
`internal/client/`; `internal/legacy` imports it until migrated); and API wire values marked
`// mtncloud:allow-upstream-name (<reason>)`. `scripts/check-names.sh` enforces this in CI on
everything that ships. Internal notes (`CLAUDE.md`, `dev-docs/`, `.claude/`) may name upstream tools.

**Customer names:** networking is **MTN Network** / **MTN Router** (`mtncloud_network`,
`mtncloud_router`). Object storage is **MOS**: customers never see "S3", only "S3-compatible".

## Domain model (summary; details in dev-docs)

| Concept | API | Facts that matter |
|---|---|---|
| Tenant | `/api/accounts`, `/api/whoami` | Login `subdomain\username`. Master vs sub-tenant gates appliance config (D-004). |
| Project = resource pool | catalog order "Create Project" (`001034sd`) → `/api/zones/{id}/resource-pools` | Renamed `<name>-<TENANCY>`; ready in about 10 min. |
| MTN Network | `/api/networks` | CIDR, gateway, DNS, DHCP, project. IPv6 off. |
| MTN Router | `/api/networks/routers` | Neutron router: external network (public IP) + internal subnets. |
| Security group | `/api/security-groups` (+ `/rules`, `/locations`) | Needs a location (project) to exist in OpenStack. |
| Instance | `POST /api/instances` | Plan change restarts the VM; root disk can't grow; primary NIC immutable. Floating IP via `/api/networks/floating-ips`. |
| Catalog order | `/api/catalog/orders` | Project, VPN, firewall, NAT, MOS, managed DB/MQ/K8s, BYOL appliances... |
| MOS | catalog item 13 (`s32311`) + `/api/storage-buckets` (type `s3`) + Cypher | Ordering keys **rotates the tenant-wide key**. Adopt existing keys, never re-order silently (D-007). |
| Cypher | `/api/cypher` | Secrets store; MOS keys live here. |
| Load balancer | Octavia, API unknown | On hold (D-013). |

## Scope

- **Removed** (D-002): AWS/Azure/vSphere/standard/MVM clouds and instances, ARM and CloudFormation
  blueprints and spec templates, `cloud_datastore`, `cloud_folder`. A test keeps them out.
- **Kept** (renamed): Kubernetes/Helm/Terraform blueprints and spec templates, **all integrations**,
  **all appliance configuration (master tenant only)**, tenancy/IAM, automation, catalog, policies,
  plans/prices, Cypher, key pairs, IP pools, scale thresholds.
- **New** (Plugin Framework, build order in [backlog](dev-docs/backlog.md)): catalog data sources,
  security group, MTN Network, MTN Router, OpenStack instance, project, catalog order, MOS, app,
  load balancer, SSL certificate.

## Architecture

```
main.go                     # serves legacy + Framework halves through terraform-plugin-mux
internal/
  legacy/                   # upstream SDKv2 code, renamed; shrinks as resources migrate
  provider/                 # Plugin Framework provider: config, auth, whoami, mux
  client/                   # the ONLY layer that does HTTP: retries, rate limit, typed errors
  tenancy/                  # master vs sub-tenant decisions
  services/<area>/          # Framework resources + data sources (compute, network, catalog, storage, iam, ...)
  validators/ planmodifiers/ normalize/
  acctest/ sweep/           # acceptance helpers and leak sweepers
dev-docs/  docs/ (generated)  examples/  templates/  scripts/  tools/
```

Resources never build URLs or JSON; they call typed `client` methods. The client knows nothing about
Terraform. Expand and flatten are pure functions with unit tests. Service packages don't import each
other.

## Commands

```
make build | test | lint | fmt | docs | docs-check | names | examples | ci
make testacc        # real MTN Cloud: only with explicit permission, test tenant only
```
