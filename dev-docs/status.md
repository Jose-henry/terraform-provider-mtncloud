# Status: what's done, what's next

Last updated 2026-11-01 (v0.9.0). Update this file whenever a slice lands. The detailed list is
[backlog-open.md](backlog-open.md); history is in git and `CHANGELOG.md`.

## Where things stand

- Released: **v0.9.0** (2026-11-01). `main` has nothing unreleased.
- 123 resources and 111 data sources, all usable by a customer (sub-tenant) login. Generated list:
  `docs/guides/features.md`.
- Live QA runs in the dev test tenant (`tf-acc-*` objects in project `admin`, each logged and deleted
  with an API 404 check). Every resource below marked "yes" passed apply, then a plan with
  `No changes.`, then destroy.
- Work goes straight to `main` at the owner's request (no PRs for now). Every slice still runs all
  gates and the `mtncloud-code-reviewer` agent, and every new test is mutation-checked. Pushes wait
  for that slice's live QA.

## Built (Plugin Framework, `internal/services`)

| Area | Types | Proven live |
|---|---|---|
| Projects | `mtncloud_project` (catalog order; destroy refuses, no delete order exists) | yes |
| Networking | `mtncloud_network`, `mtncloud_router`, `mtncloud_security_group`, `mtncloud_security_group_rule` | yes; security group rules wait on an MTN bug (B-009) |
| Instances | `mtncloud_instance`: names not IDs, volumes, floating IPs (one per server), scale, expiry, power | yes, including 2 servers with 2 public IPs (2026-11-01) |
| Day-2 | `mtncloud_instance_settings`, `mtncloud_app_settings`, `mtncloud_app`, scale schedules | yes |
| Workflows, deployments | `mtncloud_instance_workflow`, `mtncloud_deployment`, `mtncloud_deployment_version` | yes (2026-11-01) |
| Catalog services | typed order resources (databases, stacks, web servers, managed DB/MQ/streaming, self-managed Kubernetes, SIEM, VPN, WireGuard, bastion, Fortinet, PfSense, SOPHOS) | yes, except SOPHOS and FortiADC (MTN's workflow never started; owner closed B-029 without them) |
| Shares | `mtncloud_nfs_share`, `mtncloud_smb_share` (form rules checked at plan) | yes (2026-11-01) |
| MOS | `mtncloud_mos_access`, buckets | yes; object lock waits on MTN (B-022) |
| Load balancers | load balancer, listeners, instance and cluster members (D-031) | yes |
| Certificates | `mtncloud_ssl_certificate` (write-only key) | blocked: web firewall 403 |
| Cypher | `mtncloud_cypher_password`, `_uuid`, `_key` (+ legacy `_secret`, `_tfvars`) | yes; tfvars values with quotes hit the web firewall |
| Clusters | `mtncloud_cluster` (Kubernetes, Docker) | on hold (see backlog-open.md) |
| Lookups | 111 data sources | yes |
| Provider | project, group and cloud defaults (D-027); token, token file or password login | yes |

Legacy (SDKv2, `internal/legacy`), kept and renamed: tasks, workflows, jobs, schedules, scale
thresholds, credentials, key pairs, users, roles, groups, option types/lists, forms, identity sources,
contacts, environments, wiki pages, network domains, IP pools.

## Next

Open items are in [backlog-open.md](backlog-open.md). On hold with the owner: `mtncloud_cluster` and,
with it, `mtncloud_managed_kubernetes` as a Kubernetes cluster resource (B-035) and cluster layouts by
code (B-033). Waiting on MTN: security group rules (B-009), MOS object lock (B-022).

## Owner actions (ask MTN)

- Remove the SOPHOS Heat stack left by QA order 2758. Its floating IP keeps QA router 1271 and
  networks 690/691 from deleting.
- The web firewall blocks `POST /api/certificates` and Cypher tfvars values with double quotes.

## Private use-and-test guide

`private/MTN-Cloud-Terraform-Use-and-Test-Guide.pdf` (git-ignored), rebuilt with
`python private/make_guide_pdf.py .` from the examples, so its examples match the code.
