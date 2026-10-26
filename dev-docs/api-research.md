# API research (read before building a resource)

Rule: every resource is built from documentation plus a real captured payload, never from memory.
Sources, in order:

1. Context7 `/websites/apidocs_morpheusdata_reference` (the platform's OpenAPI reference, v9.0.1).
2. A real payload from MTN Cloud, captured read-only with the CLI (`-V -j` prints the request and
   the JSON). These go in `internal/client/testdata/<area>/` as contract-test fixtures, **with IDs,
   IPs, names and emails scrubbed**.
3. The automation scripts in MTN-Cloud-Service-Automation (what MTN's own workflows really send).

Where docs and a real payload disagree, the payload wins and the difference is noted here.

## MTN Network (`POST /api/networks`)

- Body `{"network": {...}}`. Required: `name`, `zone.id` (cloud), `type.id` (network type).
- Fields that matter for OpenStack: `zonePool.id` (project), `cidr`, `gateway`, `dnsPrimary`,
  `dnsSecondary`, `dhcpServer`, `allowStaticOverride`, `pool` (IP pool), `visibility`,
  `resourcePermission`, `config` (type-specific).
- Omitting `site` means the network is shared across groups. We will always send it explicitly
  (isolation rule).
- **Unknown:** the OpenStack network type ID/code on MTN, and whether subnet creation is implicit or
  a separate `POST /api/networks/{id}/subnets`. **Needs payload P1.**

## MTN Router (`POST /api/networks/routers`)

- Body `{"networkRouter": {...}}`. Required: `name`, `type.id`, `site.id` (group, or `"shared"`).
- No network server for OpenStack, so `zone.id` (cloud) is required.
- OpenStack routers come back with `routerType: "neutron"`, `externalNetwork` (the external network
  that provides the public/gateway IP), `externalIp`, and `interfaces[]` (each an internal subnet
  with `ipAddress`, `cidr`, `network`).
- Router type option types (from `GET /api/network-router-types/{id}`) for Neutron include
  `interfaces.networkSubnet.id` ("Internal Subnet", multiSelect, source `openstackInternalSubnets`,
  depends on `router.zone.id`). Option types are how this type's extra fields are defined, so the
  exact create body is type-driven.
- Plan: `external_network` (by name) to set the gateway, and `internal_subnets` (set of network
  names) for interfaces. Interfaces may become their own resource (`mtncloud_router_interface`) so
  that networks and routers can be created in any order. That is decided after P2.
- **Needs payload P2** (the router type on MTN and its option types).

## Security groups

- `POST /api/security-groups` with `{"securityGroup": {"name", "zoneId", "description",
  "customOptions": {...}, "tenantPermissions"}}`. For OpenStack, `customOptions` carries the
  project (`resourcePoolId`).
- Rules: `POST /api/security-groups/{id}/rules`, `{"rule": {"ruleType", "protocol", "direction"
  (ingress|egress), "sourceType" (cidr|group|tier|all), "source" (CIDR), "sourceGroup.id",
  "portRange" ("22" or "8000-8080"), "destinationPortRange", "name"}}`. `ruleType` and `protocol`
  are required.
- Locations: `POST /api/security-groups/{id}/locations`, `{"securityGroupLocation": {"name",
  "zoneId", "customOptions": {"resourcePoolId": <project id>}}}`. A location is where the group
  exists in OpenStack. A group with no location does not exist in any project.
- Terraform design: rules as a `set` of nested blocks on the group (one diff, no ordering churn),
  normalised (protocol lowercase, a single port written as `"22"` not `"22-22"`, CIDR canonicalised).
  A separate `mtncloud_security_group_rule` resource exists for cross-module use. The docs say never
  to mix the two for one group (same rule as the AWS provider).
- **Needs payload P3** (a real group with rules, to confirm the `ruleType` values and how OpenStack
  echoes ports back).

## Instances on OpenStack (`POST /api/instances`)

- `zoneId`, `instance.{name, type, layout.id, plan.id, site.id}`,
  `config.resourcePoolId` (project), `volumes[]` (`rootVolume`, `size`, `storageType`),
  `networkInterfaces[]` (`network.id` as `"network-<id>"`, `ipMode` static/dhcp, `ipAddress`),
  `securityGroups[] {id}`.
- Public IP: floating IPs. Allocate with `POST /api/networks/floating-ips {networkServerId,
  floatingIpPoolId}`, then attach with `PUT /api/containers/{id}/attach-floating-ip {config:
  {osExternalNetworkId: "ip-<id>" | "pool-<id>"}}`. Plan: optional `floating_ip { pool = "..." }`
  block on the instance, plus a standalone `mtncloud_floating_ip` for IPs that outlive the VM.
- MTN constraints (from Drive docs): changing the plan restarts the VM; the root disk can't grow;
  the primary NIC is immutable (`RequiresReplace`).
- **Needs payload P4** (the MTN "Create Instance" request the UI sends, to see the OpenStack
  `config` keys MTN actually uses, such as availability zone and key pair).

## Captured payloads (2026-10-26, master login, CLI 9.0.0)

Captured with `scripts/cli/capture.ps1` (reads, and `-d` dry runs only). Raw output stays in the
git-ignored `.captures/`; only the scrubbed facts below are recorded.

| # | Capture | Finding |
|---|---|---|
| P1 | `networks list` | MTN Networks are type `openstackPrivate` (id 12). Fields: `cidr`, `gateway`, `dnsPrimary`/`dnsSecondary` (MTN DNS), `dhcpServer`, `allowStaticOverride`, `zonePool {id,name}` (the project), one `subnets[]` entry named `<network> subnet`, `config {dhcpRange, hostRoutes}`. `network-types list` crashes in CLI 9.0.0 (`find_zone_by_name_or_id`), so the type comes from the list. |
| P2 | `network-routers list`, `network-routers types` | One router type: `neutronRouter` (id 1). Options: `poolId` (project, required), `interfaces.networkSubnet.id` (internal subnets, multi), `externalNetwork.id` (optional), `name`, `enabled`. Public networks (`public-network-01` .. `-05...`) are listed as `openstackPrivate` networks, so the router's external network list must come from the router option source `openstackExternalNetworks`, not a network type filter. |
| P3 | `security-groups list` | Each group has `locations[]` with `zonePool` (the project) and `externalId` (Neutron UUID). Rules: `direction`, `protocol` (`any`/`tcp`/`udp`/`icmp`), `sourceType` (`all`/`cidr`/`group`), `source`, `portRange` (`"22"`, not `22-22`), `destinationType: instance`. The synced default group is named `default` (or `<Tenant>-Default-SG`), `syncSource: external`, and is found by `locations[].zonePool.name == "<project>-<TENANCY>"` (MTN script MainUpdateSGAfterProjectCreation.sh). |
| P4 | FastCreateLinuxInstance.sh | `securityGroups: [{id}, ...]` (several, D-012); volumes `{rootVolume, name, size, storageType: 11, datastoreId: "auto"}`. |
| P5 | `service-plans list -t openstack` | 10,861 plans = 31 sizes x every project: each OpenStack project has its own flavors (`openstack.flavor.4.<projectExternalId>.<n>`). Sizes `G1S1` .. `Ge96L384` plus `LoadBalancer`; every size has a 10 GB root. **Plan lookup must be by name within the project**, never globally. |
| P6 | `storage-buckets list` | MOS buckets: `providerType: s3`, config `accessKey`, `secretKey` (masked), `endpoint`, `pathStyleAccess: on`, `region: ""`. |
| P7 | `networks floating-ips list` | 570 floating IPs, `ipStatus` (`assigned`/free), `server {id,name}`, `cloud`. Network pools: none. |
| P8 | `datastores list` | 258 datastores, all type `generic`: `__DEFAULT__` per project, plus `ENCR_AT_REST` and `_MULTIATTACH_`. See D-010. |
| P9 | `storage-volume-types list` | OpenStack: `openstackVolume` (11, resizable) and `openstackLocal` (12, flavor disk, fixed). |
| P10 | `clouds list` | One cloud: `MTNNG_CLOUD_AZ_1` (id 4, `openstack`). |

Still needed: the same reads with a **sub-tenant** login (what a customer token sees), and `-d`
dry runs of `networks add`, `network-routers add`, `security-groups add`/`add-rule`, `instances add`.

Never paste tokens, secret keys or passwords. `-V` already masks the bearer token.
