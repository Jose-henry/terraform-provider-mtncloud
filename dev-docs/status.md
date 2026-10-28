# Status: what's done, what's next

Last updated 2026-10-28 afternoon (catalog order plan fix B-019, floating IP pool check, power grace, credential fix; second QA pass in qa-2026-10-28.md). Update this
file whenever a slice lands. History before 2026-10-28 is in git and in `CHANGELOG.md`.

## Where things stand

- Released: **v0.4.0** (tagged by the owner). `main` is ahead with power control, order destroy and
  the network filter (CHANGELOG "Unreleased").
- 89 resources and 86 data sources, all usable by a customer (sub-tenant) login. Generated list:
  `docs/guides/features.md`.
- Live QA ran on 2026-10-28 in the dev test tenant: [qa-2026-10-28.md](qa-2026-10-28.md).
- Work goes straight to `main` at the owner's request (no PRs for now). Every slice still runs all
  gates and the `mtncloud-code-reviewer` agent, and every new test is mutation-checked.

## Built (Plugin Framework, `internal/services`)

| Area | Types | Proven live |
|---|---|---|
| Projects | `mtncloud_project` (catalog order; destroy refuses, no delete order exists) | yes |
| Networking | `mtncloud_network`, `mtncloud_router`, `mtncloud_security_group`, `mtncloud_security_group_rule` | yes |
| Instances | `mtncloud_instance`: names not IDs, volumes, floating IP, scale, expiry, **power_state, restart_trigger** | yes: stop, start, restart live; suspend not supported on the test image |
| Clusters | `mtncloud_cluster` (Kubernetes, Docker) | Docker yes; Kubernetes create only (token expired mid-build) |
| Catalog services | 22 typed order resources (databases, stacks, web servers, managed DB/MQ/streaming/Kubernetes, SIEM, VPN, WireGuard, bastion, Fortinet appliances) and `mtncloud_letsencrypt_certificate`. **Destroy deletes what the order made** (D-029) | nginx: order, empty plan and destroy pass live (VM and record deleted); the others share the same code |
| Shares | `mtncloud_nfs_share`, `mtncloud_smb_share` (catalog update and delete) | blocked: web firewall 403 |
| Certificates | `mtncloud_ssl_certificate` (write-only key) | blocked: web firewall 403 |
| Cypher | `mtncloud_cypher_password`, `_uuid`, `_key` (+ legacy `_secret`, `_tfvars`) | yes |
| Lookups | 86 data sources; `mtncloud_instances` filters by `network` | yes, all 86 run |
| Provider | project, group and cloud defaults (D-027); token or password login | yes |

Legacy (SDKv2, `internal/legacy`), kept and renamed: tasks, workflows, jobs, schedules, scale
thresholds, credentials, key pairs, users, roles, groups, option types/lists, forms, identity sources,
contacts, environments, wiki pages, network domains, IP pools.

## Next

The ordered list is [backlog.md B-018](backlog.md#b-018-remaining-work-after-2026-10-28-owners-list-in-order):
live power test, live order destroy, MOS (order, then buckets), `mtncloud_app` (B-017), the web
firewall error, QA second pass of automation and users, and the open follow-ups (B-016).

On hold: load balancer (D-013), `internal/tenancy` master gating (D-004, master-only types removed).

## Owner actions

- Delete the QA project `tf-acc-proj-w1hl0` with an admin login (customers can't delete projects).
- Take web firewall reference `2bE-LH4e-J8xB` (`POST /api/certificates`, `POST /api/catalog/orders`)
  to MTN's network team.

## Private use-and-test guide

`private/MTN-Cloud-Terraform-Use-and-Test-Guide.pdf` (git-ignored), rebuilt with
`python private/make_guide_pdf.py .` from the examples, so its examples match the code.
