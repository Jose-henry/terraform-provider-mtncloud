# MOS capabilities: internal notes (not customer-facing)

Owner's direction, 2026-10-29: the provider builds everything that works on our OneFS 9.15 today and
everything OneFS supports that only needs a setting or a license. Customer docs never mention the rest.
This page records the rest, for MTN's own planning. Evidence: the live probe and `isi` output in
[api-research.md](api-research.md#mos-onefs-s3-live-capability-probe-2026-10-29-akatsuki-dev-tenant);
the management summary is the Claude doc "MOS object storage: what our PowerScale can do".

## Built in the provider, but only works once MTN enables it
| Feature | Provider surface | What MTN must do |
|---|---|---|
| Object lock (GOVERNANCE only) | `object_lock_enabled` on the bucket, `mtncloud_mos_bucket_object_lock` | SmartLock license, set the compliance clock, zone Object Lock Support = Yes |
| Retention per object | `retention_mode`, `retain_until` on `mtncloud_mos_object` | Same |
| Checksums | `checksum_algorithm` on `mtncloud_mos_object` | Zone Checksum Support = Yes |

Until then the cluster answers with an error the client maps to "isn't enabled on MTN Object Storage".

## Supported by OneFS, but needs hardware or a redesign (not in the provider)
| Feature | Why not now |
|---|---|
| Encryption at rest | Needs self-encrypting drives and a key manager; nodes show SED OFFLINE, KMIP not supported. The S3 encryption header is accepted and ignored. |
| Replication to another cluster (SyncIQ) | License plus a second cluster. |
| Tiering to cloud or cheaper storage (CloudPools) | License plus a target; S3 lifecycle transitions are 501 anyway. |

## Cluster-admin features, not part of the customer S3 API, that should be enabled
These are run by MTN on the cluster. They protect customers but no customer API call reaches them.
| Feature | State | Recommendation |
|---|---|---|
| Snapshots of `/ifs/data/s3` (SnapshotIQ) | Licensed, on, no snapshots or schedules | Schedule them: the only recovery from deletion, since S3 versioning is 501. |
| Bucket quotas (SmartQuotas) | Licensed, none on S3 paths | Directory quotas per bucket path, for capacity control and usage reporting. |
| Audit forwarding | Protocol auditing on, no syslog or CEE target | Forward to the SIEM. |
| S3 syslog access logging | Off | Turn on with the audit target. |
| Consistency delay | Writes take a few seconds to reach every node; breaks the console's own bucket create | Dell ticket; check SmartConnect / the public endpoint's balancing. |

## In AWS S3, not in OneFS 9.15 (all 501, or ignored)
Bucket default encryption; SSE headers (ignored); bucket policy; bucket and object tagging;
versioning; public access block; event notifications; replication; static website; legal hold;
lifecycle tag filters; lifecycle transitions. Dell's guide also lists analytics, inventory,
metrics, transfer acceleration, requester pays, S3 Select, restore and torrent as unsupported (not
probed). The provider rejects lifecycle tag filters and transitions at plan time; the rest have no
argument at all.
