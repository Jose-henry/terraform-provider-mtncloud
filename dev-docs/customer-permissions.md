# What a customer login can read

Probed 2026-10-26 as a sub-tenant **Customer Admin User** (no `isMasterAccount` in whoami): one
`GET <endpoint>?max=1` per endpoint through `scripts/cli/capture.ps1 ... curl`. Reads only; only the
HTTP status and `meta.total` were recorded. Counts are this tenant's, for orientation only.

Rule for design: **a feature a customer uses must work with these permissions.** A 403 below means
the provider must use another endpoint (usually a wizard `/api/options/*` source, D-016), or the
feature is master-only and says so (D-004).

## Forbidden (403) for a customer

| Endpoint | Consequence |
|---|---|
| `/api/accounts` | Tenants: master-only. `mtncloud_tenant(s)` data sources can't work for customers. |
| `/api/zones` | Clouds. Use `/api/options/clouds?groupId=` (returns `MTNNG_CLOUD_AZ_1`, value 4). Legacy `mtncloud_cloud(s)` data sources fail for customers. |
| `/api/zones/{id}/resource-pools` | Projects. Use `/api/options/zonePools` (`mtncloud_projects`). |
| `/api/servers` | Hosts / hypervisors. Not a customer concept. |
| `/api/service-plans` | Plans. Use `/api/instances/service-plans` (`mtncloud_plans`). Legacy `mtncloud_plan` fails for customers. |
| `/api/price-sets`, `/api/prices` | Pricing is master-only. |
| `/api/policies` | Policies are master-only: the master tenant applies them to a sub-tenant. |
| `/api/networks/floating-ips` | Floating IPs: the customer endpoint is still unknown. |
| `/api/health` | Appliance health: master-only. |

## Readable by a customer (200)

| Area | Endpoints (count in this tenant) |
|---|---|
| Access | `users` (8), `user-groups` (1), `roles` (15), `groups` (1), `environments` (4), `approvals` (0) |
| Cloud discovery | `options/groups`, `options/clouds`, `zone-types` (10), `provision-types` (45), `data-stores` (1), `cluster-types` (2), `clusters` (0) |
| Library | `instance-types` (45), `library/container-types` (564 node types), `library/cluster-layouts` (122), `library/spec-templates` (207), `library/container-scripts` (242), `library/container-templates` (86), `virtual-images` (2) |
| Inputs and forms | `library/option-types` (0), `library/option-type-lists` (0), `library/option-type-forms` (0): readable, but the master's inputs are **not shared** with the tenant. |
| Provisioning | `instances` (6), `apps` (0), `blueprints` (0), `catalog/types` (29), `catalog/items` (11, the tenant's ordered items), `catalog/cart`, `catalog-item-types` (29), `deployments` (0) |
| Networking | `networks` (12), `networks/routers` (4), `networks/domains` (2), `networks/pools` (0), `networks/proxies` (0), `networks/servers` (2), `network-types` (68), `subnet-types` (11), `network-router-types`, `security-groups` (38), `load-balancers` (0), `load-balancer-types` (14) |
| Storage | `storage-buckets` (5), `storage-volumes` (16), `storage-volume-types` (83), `storage-servers` (0) |
| Keys and secrets | `key-pairs` (6), `certificates` (3), `credentials` (1), `cypher` (10) |
| Automation | `tasks` (2), `task-sets` (6), `jobs` (0), `job-executions` (85), `execute-schedules` (2), `power-schedules` (1), `scale-thresholds` (4) |
| Cost and activity | `budgets` (1), `invoices` (234), `usage` (791), `reports` (1), `activity` (153) |
| Monitoring | `monitoring/checks` (7), `alerts` (1), `contacts` (2), `incidents` (244) |
| VDI | `vdi-pools`, `vdi-apps`, `vdi-gateways` (all 0) |
| Integrations | `integrations` (0) |

Not probed correctly (wrong path, 404): `library/security-packages`, `network-groups`,
`networks/subnets`. Find the right paths before relying on them.

A 200 shows the role can **read**. Whether it can **create** is a separate question, answered per
resource with a `-d` dry run and, later, an acceptance test.
