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
- `dnsPrimary` / `dnsSecondary` default to MTN's resolvers `102.88.134.41` / `102.90.109.125`
  (D-017): Optional with a schema default, overridable, never required.
- Omitting `site` means the network is shared across groups. We will always send it explicitly
  (isolation rule).

### Network captures as a customer (2026-10-26, sub-tenant, reads and `-d` only)

Sources: MTN's Drive doc *Network-and-Router-Creation-Workflow-on-MTN-Cloud-v1.2* (the console
flow), the API reference (`createNetworks`), the CLI help for `networks add`, and these captures:

| Capture | Finding |
|---|---|
| `GET /api/options/creatableNetworkTypes` | One type for customers: `OpenStack Private Network` (id 12). Discover it here, never hard-code it. |
| `GET /api/network-types/12` | Customers can read it. `code openstackPrivate`, `cidrEditable: false` (the CIDR can't change: RequiresReplace), `gatewayEditable`, `dnsEditable`, `dhcpServerEditable: true`. Option fields: `cidr`, `gateway`, `dnsPrimary`, `dnsSecondary`, `config.dhcpRange`, `config.hostRoutes`, `zonePool.id` (**required**, the project). |
| `GET /api/options/networkServer?networkType.id=12` | One choice: the network server named like the cloud (`MTNNG_CLOUD_AZ_1`). The console calls it "Network Service". `/api/networks/servers` lists a second, unrelated server, so use this option list. |
| `networks add ... -N -d` | `POST /api/networks` body: `name`, `description`, `site {id}` (group), `type {id}`, `networkServer {id}`, `zonePool {id}`, `cidr`, `gateway`, `dnsPrimary`, `dnsSecondary`, `ipv4Enabled: true`, `ipv6Enabled: false`, `dhcpServer`, `allowStaticOverride`, `active`, `visibility: private`, `resourcePermission {all: true}`. |
| CLI trap | With `-N` and no project, the CLI fills in `zonePool.id` itself with the tenant's `admin` project, and most of the tenant's networks sit there. The provider makes `project` Required and always sends it. |
| CLI quirk | `--cidr` is ignored (the type's option field wins): pass `-O cidr=...`. A comma inside an `-O` value splits the argument. |
| `networks update ... -d` | `PUT /api/networks/{id}` takes a partial body (only the fields that changed). |
| `networks remove ... -d` | `DELETE /api/networks/{id}`, no body. |
| `GET /api/networks/{id}` | Echoes `zone {id,name}` (not `networkServer`), `group`, `zonePool {id,name}`, `config {dhcpRange: "", hostRoutes: ""}`, `status`, and one implicit subnet in `subnets[]` named `<network> subnet` with the same CIDR. A router attaches that subnet, so its ID is exposed. |

No network in the tenant uses `dhcpRange` or `hostRoutes`, and no document gives their format.
They are left out until a real value is seen (backlog B-010).

### Built design (owner's answers, 2026-10-27)

The owner settled the open points: IPv6 is supported but off by default; `gateway` is required;
networks (and routers) are ready when the create call returns, with no sync to wait for; and a
network still in use fails to delete with `{"success": false, "msg": "Unable to complete operation
on network <uuid>. There are one or more ports still in use on the network, id for these ports is:
<uuids>"}`. A badly designed network can also be refused by the API.

`mtncloud_network` (MTN Network)
| Attribute | Kind | Why |
|---|---|---|
| `id` | Computed, UseStateForUnknown | Server ID; import by ID. |
| `name` | Required | Updated in place. |
| `description` | Optional | Updated in place. Null stays null. |
| `project` | Required | Short or full name (D-006), checked at plan time; only another project replaces the network. Never defaulted (see the CLI trap). |
| `cidr` | Required, RequiresReplace | `cidrEditable: false`. IPv4 network address. |
| `gateway` | Required | IPv4, inside `cidr`, not its network address (plan-time check). |
| `dns_primary`, `dns_secondary` | Optional, defaults `102.88.134.41` / `102.90.109.125` | D-017. The description lists MTN's four resolvers. |
| `dhcp_enabled`, `allow_ip_override` | Optional, default `true` | MTN's doc: enable both. |
| `ipv6` | Optional object (`cidr`, `gateway` required; `dns_primary`, `dns_secondary`, `dhcp_enabled` default false) | Absent = IPv6 off. Turning it on or off, or a new IPv6 `cidr`, replaces the network (not proven to work in place); its DNS and DHCP change in place. |
| `subnet_id` | Computed | The implicit subnet, for `mtncloud_router`. |
| `external_id` | Computed | Neutron network UUID. |

Not exposed: `visibility` (always private), `active` (always true), `resourcePermission`, network
pool, network domain, `dhcp_range`, `host_routes` (B-010).

Client (`internal/client/networks.go`): `NetworkPlacement` (type and server from the two option
lists, cached, error when there isn't exactly one), `CreateNetwork` (refuses a missing project),
`GetNetwork`, `FindNetwork`, `UpdateNetwork` (PUT without CIDR, project or type), `DeleteNetwork`
(404 = done), `IsInUse`. Create never adopts an existing network of the same name and looks one up
again only after a lost reply. A create that returns a network with a failed status is reported and
kept in state, so the next apply replaces it.

Client-wide, found while building this: a 2xx response with `"success": false` is now an
`APIError` with the platform's message ("request refused: ..."), never a success.

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
- **Captured 2026-10-27** (customer login, reads and `-d` only):
  - One router type: id 1, code `neutronRouter`, "Neutron Router"; no network server, firewall, NAT,
    BGP or routing features.
  - Its option types: `name` (required), `enabled` (checkbox, required), `poolId` (project, required,
    source `zonePoolsIgnoreDefaultZoneRequired`), `externalNetwork.id` (optional, source
    `openstackExternalNetworks`), `interfaces.networkSubnet.id` (required multiSelect, source
    `openstackInternalSubnets`, depends on cloud and project).
  - A router from GET: `status: "ok"`, `routerType: "neutron"`, `enabled`, `externalIp`,
    `externalNetwork {id, code, name}`, `zone {id, name}`, `site: null`, `poolId: null` (the project is
    not echoed at the top level), `externalId` (Neutron UUID), `interfaces[]` (empty on the one read).
  - Create answers `{"success": true, "id": N}` only (API reference), so the provider reads it back.
  - The CLI cannot build the body for a customer: `network-routers add` reads `/api/zones/{id}`,
    which returns 403. The cloud comes from the option lists instead (D-018).
- **Still needed:** the wire format of `interfaces` on create (a list of `{networkSubnet: {id}}`, or
  `interfaces.networkSubnet.id` as a list?), and whether interfaces can be added and removed by PUT.
  Source: the browser request when creating a router in the console (owner), since the CLI path is
  blocked.

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

### Security group captures as a customer (2026-10-26, sub-tenant, reads and `-d` only)

Request bodies (dry runs through `--payload`, so the CLI's own lookups were skipped; they show the
documented shape, **not** that the server accepts it from a customer):

```
POST /api/security-groups                 {"securityGroup": {"name", "description", "zoneId": 4,
                                            "customOptions": {"resourcePoolId": <project id>}}}
POST /api/security-groups/{id}/locations  {"securityGroupLocation": {"name", "zoneId",
                                            "customOptions": {"resourcePoolId"}}}
POST /api/security-groups/{id}/rules      {"rule": {"name", "ruleType": "customRule", "direction",
                                            "protocol", "portRange", "sourceType", "source",
                                            "destinationType": "instance"}}
PUT  /api/security-groups/{id}/rules/{ruleId}  (exists; not needed, see design)
```

CLI quirks for customers: `security-groups add -c <cloud>` reads `/api/zones/{id}` first (403), and
`add-location` finds "0 available clouds". Both are CLI helpers; the provider sends the bodies itself.

What `GET /api/security-groups/{id}` returns (a user-made group with 20 rules and a synced
`default` group with 14), shapes only:

| Field | Seen | Design consequence |
|---|---|---|
| `ruleType` | `custom` (synced from OpenStack) and `customRule` (made in the console); `customRule: true` on both | Treat both as a custom rule; never expose `ruleType`. |
| `portRange` | `null` (all ports, also for some tcp rules), `"22"`, `"8000-8080"`. **Never `"22-22"`.** | `port_range` Optional; null means all ports. Normalise `"22-22"` to `"22"` anyway. |
| `protocol` | `any`, `tcp`, `udp`, `icmp` | Validate one of these at plan time. |
| `sourceType` | `all`, `cidr`, `group` | `all` = no `cidr` and no `source_security_group`. |
| `source` | a CIDR, **even when `sourceType` is `group`** | Read `source` only when `sourceType` is `cidr`. |
| `destination` | the remote CIDR on synced **egress** rules (`sourceType: all`) | Egress `cidr` comes from `destination`. To confirm on a rule created by the provider. |
| `sourceGroup` | `{id, name}` for group rules | Reference by ID (from `mtncloud_security_group.x.id` or the data source). |
| `destinationType` | always `instance` | Send `instance`, don't expose it. |
| `policy` | always `accept` | Not exposed. |
| `name` | null, free text, or the port (`"22"`) | Optional; null when unset. |
| `externalId` | Neutron rule UUID | Computed. |
| default egress | **every** group has two synced egress rules, `any` to `0.0.0.0/0` and to `::/0` | Neutron adds these to each new group: the design must decide what Terraform does with them (owner question). |
| group | `externalId` (Neutron UUID, or null on synced defaults), `visibility`, `syncSource`, `locations[]` with `zonePool` (the project) and `status` | Project read from `locations[0].zonePool`. No tenant permissions are returned to a customer. |

Proven by one live create with the owner's permission (2026-10-26, test project, cleaned up): a
customer can create a group, add and delete rules, and delete the group. The answers to the open
questions:

| Question | Answer |
|---|---|
| Is the group created in OpenStack straight away? | Yes: `externalId` set and the location `available` in the create response. |
| Where is an API-created egress rule's CIDR? | In `source` (`sourceType` `cidr`), same as ingress. |
| `ruleType` of an API-created rule | `customRule`; rules synced from OpenStack are `custom`. |
| Single port | Echoed as `"22"`, never `"22-22"`. |
| Default egress rules | Neutron adds allow-all egress for `0.0.0.0/0` and `::/0`, but MTN Cloud shows them only after its sync, which can take a long time. Synced form: `ruleType` `custom`, `sourceType` `all`, CIDR in `destination`. |
| Delete | `DELETE` returns 200; a later `GET` returns 404 "Security Group not found". |

### Built design (D-019)

`mtncloud_security_group`
| Attribute | Kind | Why |
|---|---|---|
| `id` | Computed, UseStateForUnknown | Server ID; import by ID. |
| `name` | Required | Updated in place (`PUT`). |
| `description` | Optional | Updated in place. Null stays null. |
| `project` | Required | Short or full name (D-006). ModifyPlan resolves both: replace only when the project ID changes. |
| `rules` | Optional, set of objects | Null = rules not managed (use `mtncloud_security_group_rule`). Set = authoritative except the default egress rules. A changed rule is deleted and recreated (OpenStack rules are immutable). |
| `rules.direction` / `protocol` | Required | `ingress`/`egress`; `tcp`/`udp`/`icmp`/`any`. |
| `rules.port_range` | Optional | Validated 1-65535, low first, `"22"` not `"22-22"`; refused for `icmp`/`any`. |
| `rules.cidr` | Optional | Must be a network address (`10.0.0.0/24`, not `10.0.0.5/24`). Conflicts with the group source. |
| `rules.source_security_group_id` | Optional | Remote group. |
| `rules.description` | Optional | Sent as the rule `name`. Unset = any name matches (no churn on synced rules). |
| `remove_default_egress` | Optional+Computed, default false | Computed only because a schema default needs it. |
| `all_rules` | Computed list | Every rule, sorted, with `default` = default egress. How users "view all the rules". |
| `external_id` | Computed | Neutron UUID. |

A configured rule shaped like a default (egress, any, no port, `0.0.0.0/0` or `::/0`) is refused at
plan time: Neutron would reject the duplicate.

`mtncloud_security_group_rule`: `security_group_id` plus the rule fields, all RequiresReplace. ID
`<group id>/<rule id>` (built and parsed in one helper), importable. Read finds the rule in the group;
gone = removed from state.

Client (`internal/client/security_groups.go`): `CreateSecurityGroup`, `GetSecurityGroup`,
`FindSecurityGroup` (idempotent create after a dropped request), `UpdateSecurityGroup`,
`DeleteSecurityGroup`, `CreateSecurityGroupRule`, `DeleteSecurityGroupRule`. Deletes treat 404 as
done. Fixtures: `internal/client/testdata/security_groups/` (scrubbed copies of the live shapes).

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

## Sub-tenant captures (2026-10-26, Customer Admin User login)

What a customer token sees. This is the role the provider must work with.

| Capture | Finding |
|---|---|
| `whoami` | No `isMasterAccount` field for a sub-tenant. The tenant **account name** (used in pool names) differs from the **login subdomain** (used in `subdomain\username`). |
| `clouds list`, `resource-pools list`, `service-plans list`, `networks floating-ips list` | **403** for this role. The provider can't use these endpoints. |
| `instances add -d` (dry run) | The wizard's endpoints all work for this role, so the lookups use them (D-016): `/api/options/groups`, `clouds?groupId`, `zonePools?zoneId` (the first item is an `isGroup` header), `zoneSecurityGroup?zoneId&poolId`, `datastores?zoneId&siteId&resourcePoolId` (the sub-tenant only sees `__DEFAULT__`), `openstackExternalNetworks?zoneId`, `/api/instances/service-plans?zoneId&layoutId&siteId&resourcePoolId` (500 without the pool). |
| `instance-types list` | 22 OS images, each with one OpenStack layout. |
| `catalog types` (`/api/catalog/types`) | No `code` field, so catalog items are picked by name. |

Still needed, as a sub-tenant, with `-d` dry runs only: `networks add`, `network-routers add`,
`security-groups add` / `add-rule` / `add-location`, and a real `security-groups get` of a group
with rules. These are the next resources.

**Floating IPs are 403 for customers** on `/api/networks/floating-ips`. Before designing
`mtncloud_floating_ip`, find the endpoint the console uses when a customer attaches a public IP.

Never paste tokens, secret keys or passwords. `-V` already masks the bearer token.
