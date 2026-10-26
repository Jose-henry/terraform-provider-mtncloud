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

## Payloads to capture (read-only; the user runs them)

| # | Command | Why |
|---|---|---|
| P1 | `morpheus network-types list -j` and `morpheus networks get <an existing MTN Network> -j` | network type code, subnet shape |
| P2 | `morpheus network-routers list -j`, `morpheus network-router-types list -j` | router type, option types, external network |
| P3 | `morpheus security-groups get <id> -j` (a group with a few rules) | rule shape as echoed |
| P4 | `morpheus instances get <id> -j --details` on a typical VM, plus the browser network tab's POST from "Create Instance" | real create payload |
| P5 | `morpheus service-plans list -j` (or `morpheus plans list -j`) | plans for OpenStack |
| P6 | `morpheus storage-buckets list -j` (one MOS bucket; **remove the secret key first**) | MOS bucket config keys |
| P7 | `morpheus networks floating-ip-pools list -j` (or `morpheus floating-ips list -j`) | floating IP pools |

Never paste tokens, secret keys or passwords. `-V` already masks the bearer token.
