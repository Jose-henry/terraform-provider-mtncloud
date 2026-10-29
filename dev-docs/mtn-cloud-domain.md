# MTN Cloud domain notes

Facts about MTN Cloud that shape the provider. Sources: the MTN Cloud Drive docs (v0.1 how-to),
the MTN-Cloud-Service-Automation repo, and read-only CLI output against
`https://console.cloud.mtn.ng` captured on 2026-10-26. IDs below are **production IDs on that
date**. They are reference material, never hard-coded in Go: the provider resolves names and codes
at plan time (see [catalog-lookups.md](catalog-lookups.md)).

## Logging in
A sub-tenant user logs in as `<tenant>\<username>`, where `<tenant>` is the tenant's subdomain **or its
numeric account ID**: both work (owner's check against the dev tenant, 2026-10-29: `<id>\<user>` gave
a token and `whoami` named the same account). So `tenant_subdomain` / `MTNCLOUD_API_TENANT` take either.

## Customer-facing names

| Customer sees | Platform object | Terraform type (planned) |
|---|---|---|
| MTN Network | OpenStack (Neutron) network | `mtncloud_network` |
| MTN Router | OpenStack (Neutron) router, external gateway + internal subnets | `mtncloud_router` |
| Security group | Security group + rules + location (project) | `mtncloud_security_group` |
| Project | Resource pool (created by the "Create Project" catalog order) | `mtncloud_project` |
| MOS (MTN Object Storage) | S3-compatible storage. Keys from catalog item 13, bucket = storage bucket of provider type `s3` | `mtncloud_object_storage` |

Docs and descriptions use the customer names. "S3" never appears in MOS user-facing text except to
say MOS is S3-compatible (so standard S3 tools work).

## MOS (MTN Object Storage)

How it works today (from `S3 Folder (Operational)` scripts in MTN-Cloud-Service-Automation):

1. Customer orders catalog item **13 "MTN Object Storage (MOS)"**, code `s32311`.
2. The workflow derives the tenant name (sanitised: lowercase, spaces to `_`, punctuation removed),
   creates a storage-cluster user for the tenant if missing, and **generates a new S3 key pair**.
   **Generating a key invalidates the previous one** for the whole tenant; all tenant users are
   emailed a notice.
3. Credentials are written to Cypher under
   `secret/MOS-<tenant>_<username>_{access_key,secret_key,private_endpoint_url,public_endpoint_url}`.
   Public endpoint: `https://<tenant-subdomain>.mos-cloud1.cloud.mtn.ng`.
4. `RevalidateMOS.sh` then rewrites every existing `s3` storage bucket that uses the access key, so
   buckets keep working after rotation.
5. The customer then creates a bucket: storage bucket, provider "MTN Object Storage" (type `s3`),
   access/secret key from Cypher, endpoint, **Create Bucket** and **Use Path Style** ticked.

Planned `mtncloud_object_storage` (design, not approved for code yet):

- One resource: ordering the keys (catalog order 13) and, optionally, one or more buckets
  (`POST /api/storage-buckets`, `providerType = "s3"`, path-style on). It reads the keys back from
  Cypher, so they never pass through user config.
- `access_key` / `secret_key` are computed and `Sensitive`. They are still in state (Terraform has
  no alternative for computed secrets), which the docs must say plainly.
- Hazard: because a new order **rotates the tenant-wide key**, Create must check Cypher for existing
  `MOS-<tenant>_*` keys and adopt them instead of ordering again unless `rotate_keys = true`. Destroy
  never revokes keys (other buckets and users depend on them). It only removes buckets it created,
  and only when `force_destroy`/empty.
- Endpoints exposed as `private_endpoint` and `public_endpoint` attributes.

## Catalog item types (ordering via `mtncloud_catalog_order`)

`morpheus catalog-item-types list` (39 items). Codes are the stable key; IDs are prod-only.

| ID | Name | Code | Notes |
|---:|---|---|---|
| 22 | Create Instance | CRINST001 | |
| 10 | Create Project | 001034sd | about 10 minutes, name gets `-<TENANCY>` suffix |
| 13 | MTN Object Storage (MOS) | s32311 | keys; see MOS above |
| 50 | MTN KUBERNETES (MK8S) | K8S | |
| 67 | MKS Managed | mks | |
| 75 | MTN DATA WAREHOUSE | K8SXBDP | |
| 51 | MTN Firewall Service | MFWSV01 | |
| 52 | NAT Service | MNTSV01 | |
| 53 | VPN Service | MVPNSV02 | |
| 16 | WireGuard | WGVPN001 | |
| 72 | SOPHOS | SPH004 | |
| 59 | Fortinet Firewall | FFW03 | |
| 68 | FortiWeb | FWB003 | |
| 69 | FortiADC | FADC003 | |
| 70 | FortiManager | FMB003 | |
| 71 | FortiAnalyzer | FAD003 | |
| 56 | Warpgate Bastion | bastion987 | |
| 66 | SIEM | WZH01 | |
| 62 | Managed Caching | Redis01 | |
| 65 | Managed Database | MDBS001 | |
| 60 | Managed MQ | MQS001 | |
| 61 | Managed Streaming | kaf01 | |
| 38 | MySQL | MySQLINST01 | |
| 39 | PostgreSQL | PoSQLINST01 | |
| 54 | MariaDB | MariaDB2378 | |
| 55 | MongoDB | MongoDBINST01 | |
| 40 | MTN Apache Web Server | APACHEWS001 | |
| 41 | Nginx | NGINXWS001 | |
| 34 | LAMP | lamp01 | |
| 35 | LEMP | Lemp01 | |
| 57 | WordPress | wordpres010 | |
| 74 | Moodle | moodle010 | |
| 73 | Let's Encrypt | letsencrypt010 | |
| 18 | NFS | nfs12 | |
| 17 | SMB | smb12 | |
| 42 | Blueprint | | |
| 4 | Feedback Form | 001034 | not infrastructure, excluded from typed helpers |
| 2 | Trouble Ticketing | 100B | not infrastructure, excluded from typed helpers |
| 49 | test variables | nfs | test item, excluded |

## Instance types customers use

`morpheus instance-types list` returned 114 types. Only the **public, OpenStack** ones matter to
customers. The rest are master-tenant private types or other clouds (excluded by the OpenStack-only
scope). Each public type below has exactly one layout.

| Type ID | Code | Name | Layout ID |
|---:|---|---|---:|
| 89 | MTN-U24.04LTS | MTN Ubuntu Server 24.04.3LTS | 309 |
| 102 | MTN-U22.04LTS | MTN Ubuntu Server 22.04.5LTS | 325 |
| 61 | MTN-D12 | MTN Debian 12 | 283 |
| 106 | MTN-D13 | MTN Debian 13 | 330 |
| 160 | MTN-AL9 | MTN Alma Linux 9.6 | 396 |
| 162 | MTN-AL10 | MTN Alma Linux 10 | 398 |
| 159 | MTN-RL9 | MTN Rocky Linux 9.6 | 395 |
| 161 | MTN-RL10 | MTN Rocky Linux 10 | 397 |
| 158 | MTN-CS9 | MTN CentOS Stream 9 | 394 |
| 104 | MTN-CS10 | MTN CentOS Stream 10 | 327 |
| 156 | MTN-F42 | MTN Fedora 42 | 392 |
| 157 | MTN-F43 | MTN Fedora 43 | 393 |
| 186 | MTN-OL7 | MTN Oracle Linux 7.9 | 454 |
| 189 | MTN-OL8 | MTN Oracle Linux 8.10 | 457 |
| 165 | MTN-OL9 | MTN Oracle Linux 9.7 | 400 |
| 181 | MTN-RHL7 | MTN Red Hat Enterprise Linux 7.9 (BYOL) | 447 |
| 188 | MTN-RHL8 | MTN Red Hat Enterprise Linux 8.10 (BYOL) | 456 |
| 166 | MTN-RHL9 | MTN Red Hat Enterprise Linux 9.7 (BYOL) | 401 |
| 167 | MTN-RHL10 | MTN Red Hat Enterprise Linux 10.1 (BYOL) | 402 |
| 80 | MTN-WINSVR2019 | MTN Windows Server 2019 (BYOL) | 300 |
| 81 | MTN-WINSVR2022 | MTN Windows Server 2022 (BYOL) | 301 |
| 164 | MTN-WINSVR2025 | MTN Windows Server 2025 (BYOL) | 399 |
| 141 | MTN-APACHEWS | MTN Apache Web Server | 372 |
| 143 | MTN-NGINXWS | MTN Nginx Web Server | 374 |
| 149 | MTN-LAMP01 | MTN LAMP Stack Server | 379 |
| 150 | MTN-LEMP01 | MTN LEMP Stack Server | 380 |
| 175 | MTN-WP01 | MTN WordPress | 440 |
| 107 | MTN-Postgres01 | MTN PostgreSQL Single-Node | 333 |
| 144 | MTN-MySQL01 | MTN MySQL Single-Node | 375 |
| 168 | MTN-MariaDB01 | MTN MariaDB Single-Node | 403 |
| 169 | MTN-MongoDB01 | MTN MongoDB Single-Node | 404 |
| 178 | managed-db | MTN Managed Database | 444, 445 |
| 182 | managed-messagin… | MTN Managed MQ | 448 |
| 180 | managed-streamin… | MTN Managed Streaming | 446 |
| 152 | MK8S-M | MTN KUBERNETES (MK8S) MASTER | 386 |
| 153 | MK8S-W | MTN KUBERNETES (MK8S) WORKER | 387 |
| 172 | warpgate | Warpgate Bastion Host | 437 |
| 177 | FFW01 | MTN Fortinet Firewall (BYOL) | 443 |
| 192 | FWB001 | MTN Fortiweb (BYOL) | 459 |
| 193 | FADC001 | MTN FortiADC (BYOL) | 460 |
| 194 | FMG001 | MTN FortiManager (BYOL) | 461 |
| 191 | FAZ001 | MTN Fortianalyzer (BYOL) | 458 |
| 155 | MVPNS012 | MTN VPN Services | 391 |
| 133 | MTN-WGVPN-01 | MTN-WIREGUARD-SSL-VPN | 364 |
| 187 | Wzh001 | MTN SIEM Service | 455 |

Notable private types: **70 "LoadBalancer"** (code `30013`, "Octavia Load Balancer Instance Type",
no provision type, no layouts). The load balancer API is still unknown (open question Q3).
`managed-db` has two layouts; which one to use must be an explicit option.

Truncated codes (`managed-messagin…`) are cut in this table only; the API returns the full code.
