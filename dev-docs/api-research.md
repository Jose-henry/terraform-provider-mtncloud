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
networks (and routers) need no sync, only their own provisioning (corrected 2026-10-27: both are
"provisioning" when the create returns and "ok" within seconds, so the provider waits); and a
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
- **Live test 2026-10-27** (owner-approved: one throwaway network and router, both deleted after;
  raw output in `.captures/router/`):
  - The network create body the provider sends (`internal/client/networks.go`) worked as is:
    `{"success": true, "errors": {}, "network": {..., "status": "provisioning"}}`, then `ok` on a
    read 15 s later, with its subnet offered to routers (no sync). The owner pointed out the
    provisioning state; the provider now waits for `ok` (`WaitForNetwork`, `timeouts { create }`).
  - Router create body that works:
    `{"networkRouter": {"name", "description", "type": {"id": 1}, "site": {"id": <group>},
    "zone": {"id": <cloud>}, "enabled": true, "poolId": <project>, "externalNetwork": {"id": <ext>},
    "interfaces": {"networkSubnet": {"id": [<subnet id>, ...]}}}}`. Answer: `{"success": true, "id": N}`.
  - **Not ready at once:** `status: "provisioning"` with no `externalId`/`externalIp`, then `ok`
    within 9 s. The provider polls to `ok` (or a failure), honouring `timeouts`.
  - Read: `site` is echoed, `poolId` is **not** (null). `interfaces[]` lists each internal network
    (`network {id, name}`, `cidr`, `ipAddress: null`) **and** the gateway (the external network,
    with the router's external IP). Internal and external are told apart by the external network ID.
  - Update (PUT, partial): `name` is refused (`400 {"errors": {"name": "Field name cannot be
    changed"}}`), so a new name replaces the router. `description` changes in place. Interfaces
    change in place by sending the full list (`[]` removes all); a PUT without `interfaces` keeps
    them.
  - Gateway (second owner-approved run, same cleanup): `{"externalNetwork": {"id": N}}` moves the
    gateway in place (new external IP, internal interfaces kept); `{"externalNetwork": null}` removes
    it, but **`externalIp` keeps the old address**, so the provider takes the external IP from the
    gateway interface, never from `externalIp`. `{"externalNetwork": {"id": ""}}` is refused (400).
  - Delete: `{"success": true}`; GET is 404 within seconds, even with an interface still attached.
  - **Network still in use:** `DELETE /api/networks/{id}` answered **200** with `{"success": false,
    "msg": "Unable to complete operation on network <uuid>. There are one or more ports still in use
    ..."}`. Without the client's success:false check this looked like a successful delete.

### Design (approved 2026-10-27: network IDs, gateway tested live)

`mtncloud_router` (MTN Router)
| Attribute | Kind | Why |
|---|---|---|
| `id` | Computed | Server ID; import by ID. |
| `name` | Required, RequiresReplace | The API refuses renames. |
| `description` | Optional | In place. |
| `project` | Required, RequiresReplace | `poolId`; short or full name, checked at plan time. Not echoed, so Read keeps the configured value and import derives it from the first internal network's project (the option list only offers that project's subnets). |
| `networks` | Required set of `mtncloud_network` IDs (at least 1) | Sent as their subnet IDs; read back from `interfaces[].network.id` (gateway excluded), so no diff. Changes in place. |
| `external_network` | Optional, name from `openstackExternalNetworks` | The gateway. Changes in place; removing it sends `null`. |
| `enabled` | Optional, default `true` | In place. |
| `external_ip` | Computed | From the gateway interface; null without a gateway (`externalIp` goes stale). |
| `external_id` | Computed | Neutron UUID. |

Update sends the gateway only when it changes (a PUT without `externalNetwork` keeps it; whether
re-sending the same one keeps the external IP is unproven). Create polls until `ok`
(`failed`/`error` stop it with the platform message); delete polls until
404. Both honour `timeouts {}` (default 10 min). No separate interface resource: the API has no
interface endpoints, and two resources rewriting one list would fight.

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

### Seen live (QA 2026-10-28)
- The cloud sync re-creates rules made through the API with new IDs and names set to the port,
  restores rules deleted before the first sync, and sets a blank description to the group's name
  (the provider keeps an unset description unset: `79f893f`).
- Groups made through the API don't appear in `/api/options/zoneSecurityGroup` (the instance
  wizard's list) for 70+ minutes, so a new instance can't use them yet.

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
- ~~Needs payload P4~~ Captured 2026-10-27 (sub-tenant, reads and a `-d` dry run only):
  - CLI create body: `{zoneId, instance {name, site {id: group}, type, instanceType {code},
    layout {id}, plan {id}}, config {createUser: true, ...}, volumes [{id: -1, rootVolume: true,
    name: "root", size, storageType: 11, datastoreId: "auto"}], networkInterfaces [{network {id:
    "network-<id>"}, ipMode: "dhcp"}], layoutSize: 1}`.
  - Instances made in the console keep in `config`: `resourcePoolId: "pool-<id>"`,
    `securityGroups: [{id}, ...]` (or one `securityGroup`), `availabilityZone`,
    `osExternalNetworkId: "pool-<id>"` (floating IP at create), `serverGroup`, `noAgent`. So these
    belong in `config`, not at the top level where the CLI puts `-O` values.
  - OpenStack provision type (33) options: `availabilityZone` (source `openstackAvailabilityZone`),
    `securityGroup` (`openstackSecurityGroupById`), `serverGroup` (`openstackServerGroup`),
    `noAgent`, `osExternalNetworkId` (`openstackFloatingIpOptions`). The two option sources answer
    500 to `zoneId/siteId/resourcePoolId` queries: their parameters are still unknown.
  - GET instance: `status`, `statusMessage`, `errorMessage`, `interfaces[] {network {id, name}}`,
    `volumes[] {id, name, size, rootVolume, storageType, datastoreId, resizeable}`,
    `connectionInfo[] {ip, port, name}` (the public IP when one is attached), `plan {id, code,
    name}`, `layout`, `instanceType {id, code, name}`, `hostName`, `containers[]`, `servers[]`.
  - Statuses (owner, 2026-10-27): instances and apps go through `provisioning`, and end in
    `running`, or in `failed` / `error`.
- More create keys (CLI dry run 2026-10-27, upstream provider, Context7):
  `instance.{hostName, instanceContext (environment code), labels[], expireDays, shutdownDays,
  networkDomain {id}}`, top level `tags [{name, value}]`, `evars [{name, value}]`, `copies`,
  `layoutSize` (scale factor), `createBackup`, `taskSetId` (provisioning workflow),
  `config.noAgent`. After create: `PUT /api/instances/{id}` (name, description, instanceContext,
  labels, tags), `PUT .../resize` (plan, volumes, networkInterfaces), `POST .../security-groups
  {securityGroupIds}`, `PUT .../threshold {instanceThreshold}`, `POST .../deploys {appDeploy}`.
- Naming (platform docs via NotebookLM): instance-name and hostname policies are separate, either
  "user configurable" or "fixed"; with no name sent, the policy pattern names the instance.
  Customers get 403 on `/api/policies`, so the provider can't read the pattern beforehand.
- Floating IP rules (owner, 2026-10-27): only possible when the instance's network is attached to
  a router with an external network; it can be attached after provisioning. Customers get 403
  on `/api/networks/floating-ips`, so the attach call a customer can make is still unknown.
- **Seen live (QA 2026-10-28):** an instance's `floating_ip_pool` attaches at create (about 3m40s) and
  removing it detaches in place. API-made instances read `environmentPrefix: null` and NIC IDs `null`.
  A resize that resends the existing `networkInterfaces` adds a duplicate card: send them only when a
  network is added (`0424084`).
- MTN docs read: "VM Provisioning Workflow" (default security group at create, more groups after;
  fault domain = availability zone; Windows root at least 40 GB; agent install needs a router),
  "Reconfiguring Instances" (plan change restarts the VM; root disk can't grow; primary NIC can't
  change; data volumes and extra NICs can be added), "Instance Access & Security" (user and
  password live in User Settings, not on the instance).

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

### Instance scaling, lifecycle, workflows (captured 2026-10-27, customer login)
- `GET /api/instances/{id}/threshold` works for customers. The answer is `{"instance":{id,name},
  "instanceThreshold":{...},"instanceSchedules":[]}`, not the reference's `instance.threshold`. A
  never-scaled instance reads `id: null`, `autoUp/autoDown: false`, `minCount: 1`, `maxCount: null`,
  `scaleIncrement: 1`, every metric disabled with null marks. Marks are floats (`30.0`).
- `PUT /api/instances/{id}/threshold` `{instanceThreshold:{autoUp, autoDown, minCount, maxCount,
  scaleIncrement, cpuEnabled, minCpu, maxCpu, memoryEnabled, ..., diskEnabled, ...}}` (Context7);
  not yet sent live.
- `GET /api/instances/{id}` carries `expireDays`, `expireDate`, `shutdownDays`, `shutdownDate` (null
  when unset), plus warning dates and counts. After create the API only offers
  `PUT .../extend-expiration`, `.../extend-shutdown` (by the policy's renewal days) and cancel.
- `GET /api/instances/{id}/deploys` works (empty); `/api/deployments` is empty for the tenant.
- `/api/task-sets`: 6 workflows, all `type: operation` (see B-014).

### Floating IPs for customers (captured 2026-10-27)
- `GET /api/options/openStack/openstackFloatingIpOptions?containerId=<vm>` works for customers (the
  earlier 500s had no `containerId`; `/api/options/openstackFloatingIpOptions` still answers 500).
  It lists headings (value `""`), pools (`{"name":"public-network-02","value":"pool-43"}`) and free
  addresses (`"ip-6420"`). Pool IDs differ from the external networks' option IDs (`public-network-02`
  is 32 there), but the names match, so plan time checks names against `openstackExternalNetworks`.
- Checked live 2026-10-30 (sub-tenant token): the cluster form's call
  `?zoneId=<cloud>&resourcePoolId=<project pool>` answers 200 with pools and free addresses, and so
  do `cloudId=` and `config.resourcePoolId=pool-<id>`. With `zoneId` alone it answers 200 and an empty
  list, not 500: only the path without `openStack/` answers 500. **Pool IDs are per project**: in the
  `admin` project `public-network-01` is `pool-44`, in another project it is `pool-1487`. So the
  provider resolves pools by name in the project's own list (clusters, instances). Catalog orders send
  the value from MTN's own form option list (`pool-44` .. `pool-42`, the `admin` project's IDs) for
  every project, which is what the console sends too.
- `GET /api/containers/{id}` works: `ip` is the floating address, `internalIp` the private one. The
  instance lists its containers in `containers`.
- Attach: `PUT /api/containers/{id}/attach-floating-ip {config:{osExternalNetworkId:"pool-<id>"}}`;
  detach: `PUT /api/containers/{id}/detach-floating-ip` (Context7). Not yet sent live.
- The apply waits for each attach and detach to show on the instance (`connectionInfo`), like a
  network's provisioning status, and fails if it doesn't (owner, 2026-10-27: an attach that fails,
  fails the apply that ran it). A 5xx on the call is followed by the same wait, which tells whether it
  worked. With every attach confirmed, an instance with no address is drift and is re-attached.
- `/api/networks/floating-ips` (allocate, list) stays 403 for customers, so a standalone floating IP
  resource that outlives an instance isn't possible with a customer login.

### Project order (captured 2026-10-27, dry run only)
- Catalog type "Create Project" (id 10, `workflow`, context `appliance`). Form fields in `config`:
  `groupName` (group, required), `cloud` (cloud, required), `projectName` (required, verifyPattern
  `^[a-z][a-z0-9_-]*$`), `projectDescription` (optional).
- The CLI's dry run printed `{"item":{"config":{...},"type":{"name":"Create Project"},"context":"appliance"}}`
  (its own cart-less shape); the provider sends the API reference's `{"order":{"items":[...]}}`.
- The workflow script (MTN-Cloud-Service-Automation, `Project Resource Pool Folder`) builds the pool
  name `<projectName>-<tenant>` and POSTs `/api/zones/{cloud}/resource-pools`.
- Inventory items for projects: `GET /api/catalog/items/{id}` gives `type.name`, `status`
  ("ORDERED") and `execution` (null on the existing ones). See D-022 and B-015.

## Catalog item orders (read 2026-10-28)

Sources: a customer's `GET /api/catalog/types/{id}` for every item (`.captures/au-cat-<id>.txt`),
MTN-Cloud-Service-Automation (`MYSQL Install (Provisioning)/CreateMysqlSingle-NodeInstance.sh` and
siblings), and `catalog add-order ... -d` dry runs.

- **Every item is a `workflow` type in `appliance` context**, the same order path as Create Project
  (`POST /api/catalog/orders`, `client.OrderWorkflow`, then `WaitForExecution`). The one exception is
  MTN Let's Encrypt SSL (73): `instance` context, so it runs against an existing instance (`target`).
- The form values reach the workflow as `customOptions.<fieldName>` and are rendered by Groovy's
  `toString`, so a JSON map arrives as `[key:value]`. The scripts parse that text with regexes.
  What they accept (comments in the scripts show what the console sends):

  | Field (widget) | Send | Script reads |
  |---|---|---|
  | `group-vpn` (group) | group ID | used raw as `"group": {"id": ...}` |
  | `cloud` (cloud) | cloud ID | `"zoneId": ...` |
  | `resourcepool` (resourcePool) | `"pool-<id>"` | `"resourcePoolId"` |
  | `plan` (select) | plan ID | used raw as `"plan": {"id": ...}`: must be a bare number |
  | `networks` (networkManager) | `[{"primaryInterface": true, "network": {"id": "network-<id>"}}]` | regex `id:(network|networkGroup|subnet)-N`; the first is primary |
  | `securitygroups` (secGroup) | `[{"id": <id>}]` | regex `id:N` |
  | `floatingip` (select) | `"pool-<id>"` | `"osExternalNetworkId"` |
  | `volumes` (diskManager) | `[{"name","rootVolume","size","storageType"}]` | regexes `size:`, `name:`, `rootVolume:`, `storageType:` (default 11), datastore always `auto` |
  | `labels` (textArray) | comma-separated string | split on `,` |
  | `environment` | environment code | `instanceContext` |

- The VM items' scripts then `POST /api/instances` **with the ordering user's own token**
  (`morpheus.getApiAccessToken()`), named by `customOptions.name`: the customer owns the instance,
  can read it and delete it. That is how a resource finds what its order made.
- `catalog add-order` dry runs prefix fields with `config.` (`-O config.name=...`) and stop on the
  first widget field the CLI can't prompt for without input (`volumes`): they confirm the body
  shape (`{"order": {"items": [{"type", "config", "context"}]}}`), not the widget values.
- No real VM order exists in the test tenant to compare with (inventory: Create Project and MKS
  Managed only), so the widget shapes above rest on the scripts. **First live order of each kind
  must be checked by hand before release.**

## Task types (2026-10-28)

`tasks add -d` dry runs: options whose field context is `taskOptions` go in `task.taskOptions`, those
with context `config` (plugin task types: Reverse Text, PowerScale) in `task.config`; `credential` is
`{"type": "local"}` or `{"id": N}`. Each type's allowed targets come from `/api/task-types`
(`allowExecuteLocal/Remote/Resource`). Not seen live (the test tenant has none of these tasks), so
QA must check: the `httpHeaders` format (sent as a JSON object string), `aiPrompt` (a file-content
input, sent as text), and how checkboxes come back (`"on"` or `true`; both are read).

### Task update semantics (open, check in QA)
- Unknown whether `PUT /api/tasks/{id}` merges `taskOptions`/`config` or replaces them. The platform's
  own update example sends every key, `null` for unused ones, so the provider now does the same: a
  removed option (password, `ignore_ssl`, `else_workflow`) is sent as `null`. QA: remove an option
  and confirm `GET` no longer shows it.
- `webMethod` and `puppetNodeName` are Optional+Computed: QA confirms whether the platform echoes
  its default (`GET`, the host name) when they are left out.

## Generated Cypher keys (2026-10-28)
Source: Context7 `/websites/apidocs_morpheusdata_reference` (addcypherkey, getcypherkey,
listcypherkeys). `POST /api/cypher/password/<len>/<name>`, `key/<bits>/<name>`, `uuid/<name>`
generate the value; `?ttl=` sets the lease (`0` = none). A `GET` of a missing key on these mounts
**creates** it, so the provider checks existence through `GET /api/cypher?key=<k>` (exact
`itemKey` match) before every read. Open for QA:
- **Confirmed live (QA 2026-10-28):** the stored `itemKey` keeps the size segment
  (`password/20/<name>`, `key/128/<name>`). `ttl "8h"` reads back as a lease of 28800000 ms; no ttl
  reads 0. Secret and tfvars imports work by key after `ac4cf89`.
- No `ttl`, or `ttl=0`, means no expiry (owner, 2026-10-28).
- The lease can't be read back as the string the user wrote: after an import with `ttl` set, the
  plan shows an in-place update that only records it in state (tested).

### Catalog order forms, resolved live (2026-10-28)
- `GET /api/catalog/types/{id}` gives each item's form (`form.options`, `form.fieldGroups[].options`,
  `optionTypes`); the provider reads it at plan time (`client.CatalogForm`), so no option type ID is
  hard-coded.
- A select's choices: `GET /api/options/list?optionTypeId=<field id>` works with a customer login
  (`/api/options/list/<list id>` answers 500, `/api/library/option-type-lists` is empty). Values can
  be quoted (`'Replace'`); the provider accepts the value or the shown name.
- The "Service Plans" list answers empty without its project dependency, whatever the query; plans
  are therefore listed with `/api/instances/service-plans` for the form's `config.layoutId`.
- `catalog add-order --context instance --target <id> -d` (Let's Encrypt): the item carries
  `"context": "instance", "targets": [{"id": "<id>"}]`, and checkboxes are sent as booleans.
- Assumed from the scripts, to confirm on the first live order: `hostCIDRs` (textarea) one CIDR per
  line; `labels`/`clientIps` (textArray) comma-separated; `quotaSize` in bytes.
- **Confirmed live (QA 2026-10-28):** `updateType` must be sent on **every** NFS/SMB order (create,
  update, delete) with the quoted value (`'Append'`, `'Replace'`, `'Allow All'`). Left out, the
  platform fills in the field's default (`Append`, unquoted) and refuses it: "IP Update Type value
  [Append] is not contained within the available options". Fixed in `c10499e`. The customer login
  then gets 403 on the NFS order, so the rest is still unconfirmed live.
- **Confirmed live:** the "Create Project" order completes in seconds; the pool appears and syncs
  within minutes. A customer gets 403 on `/api/zones/{id}/resource-pools/{id}` (read and delete).

### SSL certificates (internal) (2026-10-28)
- Types (`GET /api/certificate-types`): `internal` (upload), `internalCsr`, plus types this tenant can't use.
  Only `internal` is a resource; the rest stay readable through the data source.
- `internal` fields (`/api/certificate-types/1/option-types`): name, description, domainName,
  keyFile (required), certFile (required), chainFile.
- `certificates add -t internal -N -d` body: `{"certificate": {"type": "internal", "name", "description",
  "domainName", "certFile", "keyFile", "wildcard"}}`. PUT takes the same fields; a read never returns
  certFile, keyFile or chainFile, so the key is write-only and the certificate/chain are kept from config.
- Assumed until QA: a PUT without `keyFile` or `certFile` keeps what is stored, and `chainFile: ""` removes the chain (the PUT reference doesn't list chainFile).
  **Still open:** QA 2026-10-28 got 403 on `POST /api/certificates` with the customer login, so none of this
  could be checked live.

### Clusters (2026-10-28, customer login)
- Cluster types: `kubernetes-cluster`, `docker-cluster`. MTN's layouts (`/api/library/cluster-layouts`, the
  cloud's provision type, name starting `MTN `): Kubernetes Single-Master (1 master + 3 workers), HA (3),
  HA (5); Docker Cluster (1 host). Kubernetes layouts take `k8s_version` (a select; values like
  `1.35.3-1.1`), `podCidr` and `serviceCidr`, all in `config.customOptions`.
- `clusters add` can't run for a customer: it reads `/api/zones/{id}` (403). The body is built from the
  CLI source (`clusters.rb#add`) and the platform's own MKS resource: `{"cluster": {name, type, group{id},
  cloud{id}, layout{id}, description, labels, taskSetId, server{name, hostname, networkDomain, labels,
  plan{id}, volumes, networkInterfaces, securityGroups, nodeCount, visibility, config{resourcePoolId,
  nodeCount, customOptions, podCidr, serviceCidr, defaultRepoAccount, createUser, osExternalNetworkId}},
  worker{...}}}`. To confirm on the first live build (QA): the `worker` block, `networkDomain` as `{id}`,
  `securityGroups` as `[{id}]`, and the add-worker body: the API reference names `server.config.resourcePool`
  while the create form uses `resourcePoolId`, so both are sent (check added workers land in the project).
- Known limit: the cluster's cloud comes from the group (and the provider's `cloud`); a group spanning several
  clouds with a resource-level `cloud` different from the provider's fails the plan. MTN tenants have one cloud.
- Plans: `/api/options/servicePlans?zoneId&siteId&provisionTypeId&layoutId&resourcePoolId`. Customers can't
  read `/api/servers/service-plans`; without resourcePoolId the list repeats every price set (about 11,000 rows).
- Registries: `/api/options/dockerHubRegistries` (the public Docker Hub has an empty value). Workflows:
  `/api/options/taskSets?phase=postProvision`. Floating IP pools: the floating IP options source with
  `zoneId` and `resourcePoolId` (values `pool-<id>`, plus single addresses `ip-<id>`).
- Workers: `GET /api/clusters/{id}/workers`, `POST /api/clusters/{id}/servers` (add), `DELETE .../servers/{id}`.
  Autoscaler: `POST /api/clusters/{id}/configure-autoscaler` with
  `{cluster: {autoScalerEnabled, autoScalerGroups: [{minNodes, maxNodes}]}}`.

## MOS (OneFS S3) live capability probe (2026-10-29, dev tenant)

Owner approved the probe. Keys were read from Cypher (`secret/MOS-<tenant>_<user>_{access_key,secret_key,public_endpoint_url,private_endpoint_url}`)
into memory only. Every bucket was `tf-acc-mos-probe-*`, deleted afterwards, and 20 listings confirmed none were left.
Sources read first: Dell *OneFS S3 API Guide* H18293.6 (Aug 2025, OneFS 9.12), and the automation repo's
`S3 Folder (Operational)/CreateMOS-S3.sh` and `upload-mos-creds-to-cypher.sh`.

**How keys are made (automation repo):** one OneFS user per tenant (the sanitised tenant name) in access zone
`system`, created with the cluster admin API (`POST /platform/14/auth/users`). The key comes from
`POST /platform/14/protocols/s3/keys/<user>`, which generates a new key and replaces the old one, so it rotates
the tenant's key. The catalog workflow stores it in Cypher. Customers have no route to this API: a Terraform
"access key" can only order catalog item 13 (rotation) or read the Cypher entries (D-007).

**Bucket names are one namespace for every tenant.** OneFS keys S3 bucket names per access zone, and
every tenant's user is in zone `system`. A name another tenant owns answers 403 AccessDenied
(confirmed live by the owner 2026-10-30; as on AWS, where names are global); the provider reads a 403 on a name missing from the
tenant's own list as "taken by another tenant" (B-022). Per-tenant namespaces would need one access
zone per tenant, MTN's call.

**Endpoints:** the public endpoint is reachable from the internet over path-style HTTPS with SigV4.
The private endpoint (`:9021`) isn't reachable from outside MTN.
The public endpoint balances across nodes, and **each node sees bucket and config changes a few seconds late**:
- a new bucket answered `head` 404 on 11 of 12 reads in its first seconds;
- lifecycle and CORS reads returned the old value for about 2 to 10 seconds;
- objects were consistent at once.
Every create or update must read back until several reads in a row agree, and a 404 right after a create is
retried (rule 13).

| Area | Result | Notes |
|---|---|---|
| Bucket create, head, delete, list, location | works | location is null |
| Bucket ACL get/put (canned) | works | |
| Object put/get/head/delete, list v1/v2, object ACL get/put | works | user metadata kept; `ContentType` dropped (`binary/octet-stream` returned) |
| List object versions | answers | only `VersionId: null`; there are no versions |
| Bucket logging get/put | works | writes log objects into the target bucket (one test bucket held 9 after a minute), so destroy must empty the logs first or refuse |
| Lifecycle get/put/delete | works | prefix filter, legacy prefix, `Expiration.Days`, `AbortIncompleteMultipartUpload`; **tag filter and transitions: 501** |
| CORS get/put/delete | works | |
| Object lock config get | answers `ObjectLockConfigurationNotFoundError` (implemented) | not written: enabling is irreversible; retention can make a bucket undeletable |
| Object retention get | answers `NoSuchObjectLockConfiguration` (implemented) | |
| Encryption (bucket default), policy, tagging (bucket and object), public access block, notification, replication, website, versioning put, legal hold | **501 NotImplemented** | |
| `x-amz-server-side-encryption: AES256` on put-object | **silently ignored** (200, no `ServerSideEncryption` in head) | the provider must never offer SSE |

Lifecycle and CORS working, and object lock implemented, mean OneFS is 9.12 or later, probably 9.14 (lifecycle).
The admin API path `platform/14` fits that. Confirm with `isi version` (owner to run). Encryption at rest is
only cluster DARE (self-encrypting drives); confirm with `isi encryption status`.

### Cluster facts from the owner's `isi` output (2026-10-29)
OneFS 9.15.0.0 (B_9_15_0_0_001), 6 nodes. Licensed: ONEFS, HDFS, SMARTCONNECT_ADVANCED, SMARTDEDUPE,
SMARTQUOTAS, SNAPSHOTIQ. Unlicensed: CLOUDPOOLS, HARDENING, SMARTLOCK, SMARTPOOLS, SYNCIQ. S3: HTTPS
only, port 9021; zone System, root `/ifs/data/s3`, Object ACL Policy `replace`, Object Lock Support
**No**, Checksum Support **No**, syslog access logging off. No WORM domains, compliance clock not set.
SED key manager OFFLINE on all nodes, KMIP not supported. SnapshotIQ on with no snapshots; no S3 bucket
quotas; protocol auditing on with no syslog/CEE target. What this means per feature:
[mos-capabilities.md](mos-capabilities.md).

### Console bucket records (live, 2026-10-29, dev tenant, tf-acc-* only, all cleaned up)
MTN's customer flow (`S3 Folder (Operational)/SendS3_EmailCypher.html`): Infrastructure → Storage →
Buckets → Add → "MTN Object Storage", keys and endpoint from Cypher, tick **Create Bucket** and **Use
Path Style**. CLI dry run (`storage-buckets add ... -O createBucket=on -O config.pathStyleAccess=on -d`):

    {"storageBucket": {"name": "...", "providerType": "s3", "bucketName": "...", "createBucket": true,
      "copyToStore": true, "defaultBackupTarget": false, "defaultDeploymentTarget": false,
      "defaultVirtualImageTarget": false,
      "config": {"accessKey": "...", "secretKey": "...", "endpoint": "...", "pathStyleAccess": true}}}

| Test | Result |
|---|---|
| Console create with `createBucket: true` | **HTTP 400** "Error Validating Storage Provider: The specified bucket does not exist (NoSuchBucket)" — yet the bucket **was** created in MOS (12/12 heads afterwards). The console validates on a node that hasn't seen it yet. Orphan in MOS, no console record. Platform bug; customers in the UI can hit it too. |
| Create in MOS first, wait for 8 heads in a row (~4.5 s), then console create with `createBucket: false` | HTTP 200; record shows in the console (`config` keys accessKey, endpoint, pathStyleAccess, secretKey, secretKeyHash). |
| Archive (`POST /api/archives/buckets`, `storageProvider.id` = the record) then `DELETE /api/storage-buckets/{id}?removeResources=true` | HTTP 200. **The archive is deleted with it, silently** (archive GET 404 afterwards). |
| Console delete, with or without `removeResources=true` | Console record gone (404); **the bucket stays in MOS** in both cases. |

Provider design that follows: create in MOS, settle, register with `createBucket: false`; on destroy
refuse when an archive uses the record (name it), empty the bucket only with `force_destroy`, delete it
in MOS, then delete the console record. The tenant has the archive API but no archives yet.

### MOS live QA (2026-10-29, dev tenant, `tf-acc-mos-*`, all destroyed)
- **Settings writes to one bucket overwrite each other.** CORS and lifecycle put at the same moment kept
  only one (15 s later); one after the other kept both. MOS stores a bucket's settings together. The
  first apply (ACL, CORS, lifecycle and logging in parallel) lost three of four and timed out settling.
  Fix: the client takes a per-bucket lock for settings writes, and writes a setting again (up to 3
  times) when it hasn't appeared 30 s after the write (`MOSLostAfter`).
- With that: every resource and data source applied, then `No changes.`; a `source` edit uploaded again
  and kept the object's `public-read` ACL; import by name (bucket, CORS, object) then apply changed only
  Terraform-only values (`force_destroy`, `content`) without uploading again; destroy removed buckets
  and console records (checked with `storage-buckets list`).
- `checksum_algorithm = "SHA256"` works, although the cluster lists checksum support as off.
- `object_lock_enabled = true` fails: `InvalidRequest`, shown as "object lock isn't enabled on MTN Object
  Storage yet". The failed create left no bucket behind.
- Bucket create, then register: 8 s. Settings: up to 14 s each.

## Load balancer task (live reads, 2026-10-29, owner-approved)
- **Q1 (how the task is found):** `/api/tasks` ignores a `code` filter; the full list (3 tasks) has id 450,
  name "LoadBalancer Operations", code `lboperate.terraform`, type `octaviaLbOperations`, public.
- **Q4 (real payloads):** network 114, all `success`: `whoami` 2.5 s (`networks`); `list` 7.7 s (2 load
  balancers; row keys `id name description provisioning_status operating_status vip_address
  vip_display vip_floating vip_floating_known vip_network_id vip_subnet_id membersCount membersKnown
  networkId networkName created_at provider`; `canManage`, `total`, `fresh`, `stale`, `ageMs`,
  `source`); `read` 25.7 s (`loadbalancer` with `listeners[]`, `pools[]` each with `members[]` and
  `healthmonitor`, `vip_subnet_display`, `canManage`); `fip-options` 8.2 s (`floatingIpPools[]` with
  `name`, `externalId`; `availableFloatingIps[]` with `id`, `address`). Envelope keys: `success action
  caller networkAccess permissionSource result`. Scrubbed fixtures: `internal/client/testdata/lb/`.
- The console's own `/api/load-balancers` inventory is empty for this tenant.

## Load balancer writes (live probe, 2026-10-29, owner-approved, network 114, deleted afterwards)
- `create` (empty load balancer, `listeners: []`, `pools: []`): 1 m 49 s; answer `result.loadbalancer`
  (and `result.data`) at `PENDING_CREATE`, with no `pending` flag. Tags gain `mtncloud-req-<requestId>`.
- `pool-create` and `listener-create` answer `{success, steps: [{kind, name, applied, created, id}]}`,
  not the guide's `piece`; a pool created with a monitor gives two steps (`pool`, `health monitor`).
  The provider takes the ID from `piece.id` or the step of its kind.
- `delete` answers `{success, id, deleting: true}` after 31 s; a `read` then says "not found on this
  network" once it is gone. `read` takes about 2 s on a small load balancer.
- VIP subnet: the console subnet's `externalId` (`GET /api/subnets/{id}`) is the cloud subnet ID the
  plugin wants; `mtncloud_network`'s subnet is the console's.
- Guide mismatches reported to the plugin side: `steps[]` instead of `piece`, no `pending` flag, and
  `deleting` on delete.
- `loadbalancer-update` (added 2026-10-29, guide 9.6): spec keys `name`, `description`, `tags`,
  `adminStateUp` only; null leaves a field, `""`/`[]` clears; any other key is refused by name.

### Plugin answer to the provider's requests (f577a90, console-tested by the plugin side, 2026-10-29)
- Piece creates (`listener-create`, `pool-create`, `member-add`, `monitor-create`, `l7policy-create`)
  take an optional `requestId`, 1 to 64 of `A-Za-z0-9_-`. The piece is tagged `mtncloud-req-<id>`; a
  repeat answers `existing: true` with the piece and changes nothing. One ID per create attempt.
- Every create and update answers with top-level `piece` (a monitor's is the monitor, a member's the
  member), also with `pending: true`. It is missing only when `pending` is true and the save
  outlasted the plugin's 180 s wait.
- Clearing: `sniContainerRefs: []` removes the SNI refs; `description: ""` clears; `member-update`
  with `monitorAddress: null` / `monitorPort: null` removes the override. `defaultTlsContainerRef:
  null` on a TERMINATED_HTTPS listener is refused ("must keep a certificate").
- A listener takes `description` on create and update.
- "Already gone" messages include "L7 policy <id> is not on listener <listenerId>." and "Pool
  <poolId> has no members of instance <id>."
- L7: `l7policy-create` makes the policy, then its rules one by one. A refused rule rolls the new
  policy back (a step with `rolledBack: true`, answer `success: false`); a retry with the same
  `requestId` builds it whole, so a policy is never returned as existing with missing rules.

### L7 policies, instance members, floating IP (live QA, 2026-10-29, dev tenant, `tf-acc-lb-*`, destroyed)
- QA config `t91-lbmore`, 7 objects: the load balancer, 2 pools, a listener, an instance's members, a floating
  IP and an L7 policy. Stage 1 created all 7, then `No changes.` Stage 2 changed the L7 policy in place, from
  `REDIRECT_TO_POOL` to `REDIRECT_TO_URL` with code 301 and an inverted rule (1 changed), then `No changes.`
  Destroying all 7 took about 5 minutes, most of it the load balancer.
- `member-add` with `instanceId` added one member per node address of a 2-node instance. Both carry
  the plugin's group tag. The answer is `{members:[...]}`, with no single piece.
- `fip-attach` from a pool answered the address at once; the network's list row showed it in `vip_floating`.
- L7 rules come back under `l7policies_expanded[].rules_expanded`. A redirect without a code reads 302.
- Positions (t92, live): the plugin refuses a create at a position another policy holds now ("position 1
  is used by another policy on this listener"). C was asked for 3 on an empty listener, was stored at 1,
  and then blocked A at 1. Created in position order, A, B and C landed at 1, 2, 3 with `No changes.` (D-033, B-025).
  Plugin 07b5e8d (2026-10-30, console-tested by the plugin owner) inserts and shifts instead, and the
  answer carries the listener's `l7policies` after the shift as `{id, name, position, action, tags}`.
  Live QA t95 (2026-10-30): an insert at 1 and a full reorder each landed as configured in one apply,
  then `No changes.`
- Default security group (t94, live, admin project): an instance listing `default` and a WireGuard VPN
  order listing no groups both came up with exactly one group, `default`
  (`GET /api/instances/{id}/security-groups`). The instance record itself carries no groups.

## Archives (live probe, 2026-10-29, dev tenant, `tf-acc-arch-*`, all deleted)
Through the CLI on a QA MOS bucket (`mtncloud_mos_bucket`, console record from the provider):
- `archives add -d` builds `{"archiveBucket": {name, description, storageProvider: {id}, accounts: [],
  visibility, isPublic}}`. The storage provider choices come from `GET /api/options/storageProviders`
  (console record names and IDs).
- Create answers `{success, archiveBucket: {id, name, description, storageProvider: {id, name (the
  record's console name)}, owner: {id, name}, createdBy, isPublic, visibility, code, filePath
  ("morpheus-archives/<code>/", the prefix in the MOS bucket), rawSize (null when empty), fileCount,
  accounts, dateCreated, lastUpdated}}`. `GET /api/archives/buckets/{id or name}` answers the same.
- Upload: `POST /api/archives/buckets/<name>/files/<folder>?filename=<file>` with the raw body
  (`application/octet-stream`); the folder is made on the way.
- List: `GET /api/archives/buckets/<name>/files/<folder>` answers `{archiveFiles: [{id, name,
  filePath (the whole path, "docs/hello.txt"), isDirectory, status, rawSize, contentType, ...}],
  archiveFileCount, parentDirectory, archiveBucket, isOwner, meta}`. A missing folder is a 200 with
  `msg: "Directory not found."` and no files.
- `DELETE /api/archives/buckets/{id}` on an archive holding a file: `{"success": true}`; afterwards the
  MOS bucket held no objects. Confirmed in the provider's QA: an uploaded file sits in the MOS bucket
  at `<filePath>docs/hello.txt`, and is gone from the bucket after the archive is deleted.

### Archive files and links (live probe, 2026-10-29, `tf-acc-files-*`, deleted)
- Uploading the same path again overwrites the file and **keeps its ID** (same ID before and after).
- `GET /api/archives/files/{id}` answers `{archiveFile: {id, name, filePath ("docs/note.txt", no
  leading slash), archiveBucket {id, name, isPublic}, createdBy, isDirectory, status ("Active"),
  rawSize, contentType ("binary/octet-stream" for an octet-stream upload), downloadCount,
  dateCreated, lastUpdated}, isOwner}`.
- `POST /api/archives/files/{id}/links?expireSeconds=60` answers `{success, secretAccessKey}` only (no
  link ID). Without `expireSeconds` the link never expires (`expirationDate: null`); the CLI's
  `add-file-link` defaults to 1200 s and leaves the parameter out for 0.
- `GET /api/archives/files/{id}/links` answers `{archiveFileLinks: [{id, secretAccessKey,
  archiveFile {id, name, filePath}, createdBy, dateCreated, lastUpdated, lastAccessDate,
  expirationDate, downloadCount}], archiveFileLinkCount, meta}`.- The public download URL is `<console>/public-archives/link?s=<secret>`, **not** the documented
  `/api/public-archives/link` (that one answers 401 "Full authentication is required"). Checked live:
  the link downloaded the exact bytes Terraform uploaded, without a token.
