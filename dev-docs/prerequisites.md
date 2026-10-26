# Prerequisites: projects, synced defaults, and name resolution

Status: design, awaiting approval. No code yet.

Almost everything a customer creates (instance, MTN Network, MTN Router, security group, floating IP)
lives **inside a project**. A project is an OpenStack project, which the platform shows as a
**resource pool** on the cloud (`MTNNG_CLOUD_AZ_1`, zone id 4 in prod). Customers only ever see and
type the **project name**. The provider resolves it to the pool ID on the backend.

## What MTN's own docs say (Drive, V0.1_Documentations)

- **Project-Creation-Workflow v1.0:** Catalog → "Create Project" with **Group**, **Cloud** (for
  example `MTNNG_CLOUD_AZ_1`), **Project Name** (unique) and an optional description. The result is
  named `<project name>-<tenancy>`. "Wait for about ten (10) minutes before proceeding. This period is
  needed for all required resources to be made available within your tenancy." Afterwards, VM
  instances, networks, routers and security groups must all select this project in their
  **Resource Pool** field. A tenancy may have several projects.
- **Network-and-Router-Creation-Workflow v1.2:** prerequisite: a project exists. The order is
  **network first, then router**.
  - Network: Group, Network Service (the cloud), Name, Display Name, Labels, Description, IPv4 on,
    CIDR, Gateway, DNS Primary/Secondary (MTN resolvers 102.88.134.41, 102.90.109.125, 102.89.90.112,
    120.91.123.54), **Resource Pool**, optional DHCP allocation and host routes, Active, DHCP Server
    and Allow IP Override on, network pool and domain blank, **IPv6 off**. "When properly created,
    the subnet should also be visible", so the subnet is created with the network.
  - Router: type **Neutron Router**, Group, Network Service, Name, **Resource Pool**, Enabled,
    External Network (optional; only for internet access), Internal Networks (one or more of the
    networks created).
- **Instance Access & Security Management Guide v1.0:** VM login credentials come from the user's
  **User Settings** (Linux/Windows settings) on the platform. They are not set per instance, and
  the guide says to keep secrets in Cypher.

Provider consequences: `group` and `cloud` default to `MTNNG_CLOUD_AZ_1` at provider level, so a
customer usually only writes `project`. `mtncloud_network` defaults `dns_primary`/`dns_secondary`
to MTN's resolvers, `dhcp_server = true`, `allow_ip_override = true`, and has no IPv6 attributes.
`mtncloud_router` takes `external_network` (optional) and `internal_networks` (by name), so
Terraform's graph enforces "network before router" with no `depends_on`.

## What the API shows (`GET /api/zones/4/resource-pools`, 2026-10-26)

```json
{
  "id": 768,
  "name": "testproject-AcmeCorp",
  "externalId": "c6db9ce7b11a414293bbe93dc6ba1faa",
  "zone": { "id": 4, "name": "MTNNG_CLOUD_AZ_1" },
  "type": "default", "status": "available", "active": true, "visibility": "private",
  "tenants": [ { "id": 72, "name": "AcmeCorp" }, { "id": 1, "name": "MTNNG_MASTER_TENANT" } ],
  "config": { "tenancy": "default", "managers": ["cloudadmin"] }
}
```

- **Name rule:** pool name = `<project>-<tenant account name>`, with the suffix in the tenant's exact
  case: `testproject` + `AcmeCorp` gives `testproject-AcmeCorp`, and `qa` +
  `arcapaymentscompanylimitedcorp-lsq7` gives `qa-arcapaymentscompanylimitedcorp-lsq7`. The tenant
  name itself can contain `-`, so the project part is found by **stripping the known tenant suffix**,
  never by splitting on the last `-`.
- `externalId` is the OpenStack project ID. `id` is the pool ID that the API wants as
  `config.resourcePoolId`, `zonePool.id`, or the security group location's `resourcePoolId`.
- `tenants[]` always includes the owning tenant **and the master tenant**.
- A master token sees every tenant's pools (256 in prod). A tenant token sees only its own, provided
  its role has Infrastructure permissions.

## Resolution algorithm (`internal/client` + `internal/tenancy`)

Configure time (once per run): `GET /api/whoami` gives the account `{id, name}` and whether it is the
master tenant.

`ResolveProject(ctx, cloud, project)`:
1. The cloud (default `MTNNG_CLOUD_AZ_1`, overridable on the provider) resolves to a zone ID by name,
   cached.
2. `GET /api/zones/{zone}/resource-pools?name=<project>-<tenantName>` (exact full name), paging
   with `max`/`offset` if needed. The list is cached per run (one call, not one per resource).
3. Keep only pools whose `tenants[]` contains **the caller's tenant ID**. This matters with a master
   token, where another tenant could have a project of the same short name. Isolation is checked by
   ID, not trusted to names.
4. Exactly one match: return `{id, externalId, name}`. None: an error that lists that tenant's
   projects by short name. More than one: an error listing the IDs (it should never happen).
5. Also accept the full name or `id` for people who already have them. Short name is the default.

Read reports `project` as the **short** name (suffix removed), so the plan stays empty
(normalisation, rule 7). State keeps `project_id` (computed, `UseStateForUnknown`).

**Master-tenant users acting for a customer** (MTN ops): there is a provider-level `tenant` override,
which the master tenant only may set, so the suffix and the `tenants[]` filter use that tenant
instead of the master. A sub-tenant token that sets it gets a plan-time error.

## What syncs in after a project exists

Creating a project ("Create Project" catalog order, code `001034sd`, about 10 minutes) makes an
OpenStack project. The cloud's **sync** then brings its objects into the platform:

| Synced object | Why the provider cares |
|---|---|
| The resource pool itself | Appears only after sync. `mtncloud_project` Create polls until the pool exists and is `available`. |
| The project's **default security group** (OpenStack creates `default` per project) | Instances must get a security group at create. If the user names none, we attach `default`, resolved **inside this project** (several projects each have one named `default`). |
| Networks / IP pools / floating IP pools visible to the project | Lookups for MTN Network and floating IPs must filter by project, not just by name. |

Rules that follow:
- `mtncloud_project` Create is done only when the pool **and** its default security group are both
  visible (poll with backoff; default timeout 30m; the user can override with `timeouts {}`). If
  dependents start before sync, they fail with "project not found".
- A data source `mtncloud_project` (name → id, external_id, default_security_group_id) lets
  configurations depend on a project created outside Terraform.
- Resources that need a project take `project = "<short name>"` (or the `mtncloud_project`
  resource's attribute, which gives Terraform the dependency order automatically).
- **Eventual consistency:** a 404 for a synced object within the sync window is retried, not
  reported as gone (rule 13).

## Still to confirm (payloads)

- P8: `<cli> resource-pools list -c 4 -j` **with a tenant token** (not master), to confirm the tenant
  sees only its own pools and that `tenants[]` is still present.
- P9: `<cli> security-groups list -j` filtered to one project, to see how the synced `default` group
  records its project (a location with `resourcePoolId`?).
- P10: what "the pools that sync" covers in MTN's setup: IP pools, floating IP pools, or both, and
  whether they are per project.
