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

## D-005 New resources use the Plugin Framework, muxed with legacy (2026-10-26, recommended, pending confirmation)
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
