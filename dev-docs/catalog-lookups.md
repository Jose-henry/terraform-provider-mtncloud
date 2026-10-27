# Catalog lookups: no IDs from the UI

Goal: a customer writes Terraform without opening the console to copy an ID. Everything they pick
(image, size, network, project, security group, catalog item) is chosen by a **readable name or
code**, and can be discovered from Terraform itself.

Status: layer 3 (list data sources) **built** (2026-10-26). Layers 1 and 2 arrive with each resource.

## Three layers

1. **Names in resource arguments.** Resources accept the human key and resolve it at plan time:
   `image = "MTN-U24.04LTS"` (instance type code), `plan = "2 vCPU / 4 GB"`,
   `project = "web-prod"`, `network = "app-net"`. The resolved numeric IDs are exposed as computed
   `*_id` attributes. An ID is still accepted (`image_id`) for people who have one. The name and ID
   arguments are `ExactlyOneOf`. Resolution must be unambiguous: if a name matches more than one
   object, it errors and lists the candidates with their IDs.
2. **Single-item data sources** (most exist in legacy): `mtncloud_instance_type`, `mtncloud_plan`,
   `mtncloud_network`, `mtncloud_resource_pool`, `mtncloud_catalog_item_type`, and so on. Each one
   looks up by `name` or `code` and gets an error message listing the valid names when nothing matches.
3. **Catalog (list) data sources**, all new. They answer "what can I choose from?" and work with
   `for_each`:

A customer role gets **403** on the admin endpoints (`/api/zones`, `/api/zones/{id}/resource-pools`,
`/api/service-plans`, `/api/networks/floating-ips`). Every lookup therefore uses the endpoints the
console's own create-instance wizard calls, captured by dry-running `instances add` as a sub-tenant.
Cloud and group are discovered once (`/api/options/groups`, `/api/options/clouds?groupId=`); a tenant
with more than one is an error, not a guess.

| Data source | API (customer-safe) | Arguments | Returns per item |
|---|---|---|---|
| `mtncloud_projects` | `GET /api/options/zonePools?zoneId=` (skip `isGroup` headers) | `name_regex` | `id`, `name` (short), `full_name`, `external_id` |
| `mtncloud_images` | `GET /api/instance-types` (paged), category `os` only (D-011) | `name_regex` (code or name) | `id`, `code`, `name`, `layout_id` |
| `mtncloud_plans` | `GET /api/instances/service-plans?zoneId&siteId&layoutId&resourcePoolId` (500 without the project) | `project`, `image`, `name_regex`, `min_cores`, `min_memory_gb` | `id`, `name`, `code`, `cores`, `memory_gb`, `root_disk_gb` |
| `mtncloud_datastores` | `GET /api/options/datastores?zoneId&siteId&resourcePoolId` | `project`, `name_regex` | `id`, `name` |
| `mtncloud_security_groups` | `GET /api/options/zoneSecurityGroup?zoneId&poolId` | `project`, `name_regex` | `id`, `name`, `external_id` |
| `mtncloud_external_networks` | `GET /api/options/openstackExternalNetworks?zoneId=` | `name_regex` | `id`, `name` |
| `mtncloud_catalog_items` | `GET /api/catalog/types` (paged; no `code` field) | `name_regex`, `type` | `id`, `name`, `description`, `type`, `featured` |
| `mtncloud_routers` | `GET /api/networks/routers` (paged) | `name_regex` | `id`, `name`, `description`, `status`, `enabled`, `external_network`, `external_ip`, `networks` |
| `mtncloud_instances` | `GET /api/instances` (paged) + projects for names | `name_regex`, `project` | `id`, `name`, `status`, `project`, `image`, `plan`, `private_ip`, `public_ip`, `labels` |
| `mtncloud_instance` | `GET /api/instances/{id}`, or the list by exact name | `id` or `name`, `project` | the same, for one instance |

`project` accepts the short name (`web-prod`) or the full name (`web-prod-<TENANT>`), matched
case-sensitively. TENANT is the account name from `/api/whoami`, not the login subdomain: user
`acme\jdoe` belongs to tenant `AcmeCorp`, whose pools end in `-AcmeCorp`
(`internal/normalize/project.go`).

Example (target UX):

```hcl
data "mtncloud_images" "linux" {}

resource "mtncloud_instance" "web" {
  name     = "web-1"
  project  = "web-prod"
  image    = "MTN-U24.04LTS"
  plan     = "G2S4"
  networks = [mtncloud_network.app.name]
}

output "available_images" {
  value = { for i in data.mtncloud_images.linux.items : i.code => i.name }
}
```

## Why codes, not IDs, are the stable key

IDs are per-environment database keys (the same image has a different ID on a staging appliance),
and they change if an item is recreated. Codes (`MTN-U24.04LTS`, `s32311`) are set by the MTN cloud
team and survive both. So:

- The provider never hard-codes a numeric ID in Go.
- The tables in [mtn-cloud-domain.md](mtn-cloud-domain.md) are reference material for humans and
  the source for a docs page `docs/guides/catalog.md`. A script (`scripts/gen-catalog-guide`,
  read-only, run by a maintainer with credentials, never in CI) regenerates that page from the live
  API, so it does not go stale silently.
- Validators may offer **suggestions** ("did you mean MTN-U24.04LTS?") from the live list, never a
  fixed enum. A fixed enum would break every time MTN publishes a new image.

## Performance rules for lookups

- One list call per kind per provider run: an in-memory cache keyed by (kind, filters) in
  `internal/client`, invalidated on any write of that kind. 50 instances using the same image do
  not mean 50 `GET /api/instance-types` calls.
- Lookups run in `ModifyPlan`/`Read` only when the name changed or the ID is unknown. They are not
  repeated on every refresh once the ID is in state.
