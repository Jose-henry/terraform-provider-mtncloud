# Catalog lookups: no IDs from the UI

Goal: a customer writes Terraform without opening the console to copy an ID. Everything they pick
(image, size, network, project, security group, catalog item) is chosen by a **readable name or
code**, and can be discovered from Terraform itself.

Status: design, awaiting approval. No code yet.

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

| Data source | API | Filters | Returns per item |
|---|---|---|---|
| `mtncloud_images` | `GET /api/instance-types` | `category`, `name_regex`; default public + openstack | `code`, `name`, `id`, `layout_ids`, `category`, `versions` |
| `mtncloud_plans` | `GET /api/service-plans` (filter by provision type openstack) | `name_regex`, `min_cores`, `min_memory_gb` | `id`, `name`, `code`, `cores`, `memory_gb`, `disk_gb` |
| `mtncloud_catalog_items` | `GET /api/catalog-item-types` | `name_regex`, `label` | `id`, `code`, `name`, `labels` |
| `mtncloud_projects` | `GET /api/zones/{cloud}/resource-pools` (caller's tenant only) | `name_regex` | `id`, short `name`, `full_name`, `external_id` (see [prerequisites.md](prerequisites.md)) |
| `mtncloud_networks` (exists) | `GET /api/networks` | add `project` filter | |
| `mtncloud_security_groups` | `GET /api/security-groups` | `project`, `name_regex` | `id`, `name`, `rule_count` |
| `mtncloud_routers` | `GET /api/networks/routers` | `project` | `id`, `name`, `external_network`, `external_ip` |
| `mtncloud_floating_ip_pools` | `GET /api/networks/floating-ip-pools` | | `id`, `name` |

Example (target UX):

```hcl
data "mtncloud_images" "linux" {
  category = "os"
}

resource "mtncloud_instance" "web" {
  name     = "web-1"
  project  = "web-prod"
  image    = "MTN-U24.04LTS"
  plan     = "MTN-2C4G"
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
