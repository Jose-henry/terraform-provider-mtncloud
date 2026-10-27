# Catalog order forms (read 2026-10-27)

Every item a customer can order, from a customer login's `GET /api/catalog/types/{id}` (read-only
captures `.captures/au-cat-<id>.txt`; field names only, no tenant data). The input names are the
keys an order sends in `config.customOptions`. `mtncloud_catalog_item` shows the same at plan time.

Most items deploy VMs and share a **base form**: `group-vpn`, `cloud`, `name`, `resourcepool`
(project), `plan`, `volumes`, `networks`, `securitygroups`, `floatingip`, `environment`, `labels`.
The columns below list what each item adds to it (required, then optional).

| ID | Item | Base form | Own inputs (required) | Own inputs (optional) |
|---|---|---|---|---|
| 22 | Create Instance | yes | `instancetype` | `layouts`, `scalefactor` |
| 10 | Create Project | no | `groupName`, `projectName` | `projectDescription` |
| 40 | MTN Apache Web Server | yes | - | `webServerPort`, `scalefactor`, `labelss` |
| 69 | MTN FortiADC (BYOL) | yes | - | - |
| 71 | MTN FortiAnalyzer (BYOL) | yes | - | - |
| 70 | MTN FortiManager (BYOL) | yes | - | - |
| 59 | MTN Fortinet Firewall (BYOL) | yes | - | - |
| 68 | MTN FortiWeb (BYOL) | yes | - | - |
| 50 | MTN KUBERNETES (MK8S) | no | `master_plan`, `master_volumes`, `master_securitygroups`, `master_network` | `single_node_cluster`, `master_floatingip`, `k8s_master_count`, `worker_plan`, `worker_volumes`, `worker_securitygroups`, `worker_network`, `worker_floatingip`, `k8s_worker_count`, `cni_provider`, `k8s_api_endpoint` |
| 34 | MTN LAMP (PHP) | yes | `hostCIDRs` | `webServerPort`, `mysqlVersion`, `dbName`, `dbUsername`, `dbPassword`, `makeUserDbOwner`, `requireSSL`, `useHostSSL`, `passwordExpiryDays`, `autoSecurityUpdates`, `php_version`, `php_memory_limit`, `php_upload_max` |
| 35 | MTN LEMP (PHP) | yes | `hostCIDRs` | `webServerPort`, `mysqlVersion`, `dbName`, `dbUsername`, `dbPassword`, `makeUserDbOwner`, `requireSSL`, `useHostSSL`, `passwordExpiryDays`, `autoSecurityUpdates`, `php_version`, `php_memory_limit`, `php_upload_max` |
| 73 | MTN Let's Encrypt SSL | no | `sslDomainName` | `sslAdminEmail`, `sslStaging` |
| 65 | MTN Managed Database | yes | `db_engine`, `adminUsername`, `adminPassword` | `mysqlVersion`, `pgVersion`, `no_nodes` |
| 60 | MTN Managed MQ | yes | `no_nodes`, `adminUsername`, `adminPassword` | `msg_engine`, `rabbitmqVersion` |
| 61 | MTN Managed Streaming | yes | `adminUsername`, `adminPassword` | `kafkaVersion` |
| 54 | MTN MariaDB | yes | `mariadbVersion`, `hostCIDRs` | `dbName`, `dbUsername`, `dbPassword`, `makeUserDbOwner`, `requireSSL`, `useHostSSL`, `passwordExpiryDays`, `autoSecurityUpdates` |
| 55 | MTN MongoDB | yes | `mongodbVersion`, `adminUsername`, `adminPassword`, `hostCIDRs` | `dbName`, `dbUsername`, `dbPassword`, `mongoUserRole`, `enableAuth`, `requireSSL`, `autoSecurityUpdates`, `initReplicaSet`, `replicaSetName`, `wiredTigerCacheGB`, `installMongosh`, `installDatabaseTools`, `installAtlasCLI`, `installMCPServer`, `installMongosync`, `installBIConnector` |
| 38 | MTN MySQL | yes | `hostCIDRs` | `mysqlVersion`, `dbName`, `dbUsername`, `dbPassword`, `makeUserDbOwner`, `requireSSL`, `useHostSSL`, `passwordExpiryDays`, `autoSecurityUpdates` |
| 18 | MTN NFS | no | `volumeName` | `updateOnly`, `deleteOnly`, `updateQuota`, `quotaSize`, `updateClientIps`, `updateType`, `clientIps` |
| 41 | MTN Nginx Web Server | yes | - | `webServerPort`, `scalefactor`, `labelss` |
| 13 | MTN Object Storage (MOS) | no | - | - |
| 39 | MTN PostgreSQL | yes | `hostCIDRs` | `pgVersion`, `dbName`, `dbUsername`, `dbPassword`, `makeUserDbOwner`, `requireSSL`, `useHostSSL`, `passwordExpiryDays`, `autoSecurityUpdates` |
| 17 | MTN SMB | no | `volumeName` | `updateOnly`, `deleteOnly`, `updatePassword`, `smbPassword`, `updateQuota`, `quotaSize`, `updateClientIps`, `updateType`, `clientIps` |
| 53 | MTN VPN Service | yes | - | `volume` |
| 16 | MTN WireGuard SSL VPN | yes | - | - |
| 57 | MTN WordPress | yes | `webserver_type`, `dbName`, `dbUsername`, `dbPassword`, `makeUserDbOwner`, `hostCIDRs`, `php_version`, `php_memory_limit`, `php_upload_max` | `webServerPort`, `mysqlVersion`, `autoSecurityUpdates` |
| 56 | Warpgate Bastion Host | yes | `warpgateAdminPassword` | - |
| 66 | MTN SIEM Service | yes | `wazuhVersion`, `enableLogall`, `wazuhAdminPassword` | - |
| 2 | Trouble Ticketing | no | `ticketType`, `ticketShortdescription`, `ticketDescription`, `ticketSeverity` | - |
