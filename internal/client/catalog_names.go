package client

import "strings"

// CatalogItemName is a catalog item's name now, and the names it had before.
type CatalogItemName struct {
	Name   string   // as MTN Cloud's catalog shows it now
	Former []string // old names: still matched, deprecated, removed in a later release
}

// CatalogNames are the catalog items this provider orders, by code. MTN renamed most of the
// catalog on 2026-11-06 and again by 2026-11-17 (read with the CLI, /api/catalog-item-types).
// Items are found by code; the names are for logins that only see the customer catalog, for
// orders placed before a rename, and for messages.
var CatalogNames = map[string]CatalogItemName{
	"001034sd":       {"Project", []string{"Create Project"}},
	"s32311":         {"MTN Object Storage", []string{"MTN Object Storage (MOS)"}},
	"nfs12":          {"MTN File Storage · NFS", []string{"MTN NFS Storage", "MTN NFS"}},
	"smb12":          {"MTN File Storage · SMB", []string{"MTN SMB Storage", "MTN SMB"}},
	"MySQLINST01":    {"MTN MySQL Server", []string{"MTN SQL Database - MySQL", "MTN MySQL"}},
	"PoSQLINST01":    {"MTN PostgreSQL Server", []string{"MTN SQL Database · PostgreSQL", "MTN SQL Database - PostgreSQL", "MTN PostgreSQL"}},
	"MariaDB2378":    {"MTN MariaDB Server", []string{"MTN SQL Database - MariaDB", "MTN MariaDB"}},
	"MongoDBINST01":  {"MTN MongoDB Server", []string{"MongoDB Database", "MTN MongoDB"}},
	"lamp01":         {"MTN LAMP Stack", []string{"MTN LAMP (PHP)"}},
	"Lemp01":         {"MTN LEMP Stack", []string{"MTN LEMP (PHP)"}},
	"wordpres010":    {"MTN WordPress Hosting", []string{"MTN Website Hosting (WordPress)", "MTN WordPress"}},
	"APACHEWS001":    {"MTN Web Server - Apache", []string{"MTN Apache Web Server"}},
	"NGINXWS001":     {"MTN Web Server - Nginx", []string{"MTN Nginx Web Server"}},
	"MDBS001":        {"MTN Database Service", []string{"MTN Managed Database"}},
	"MQS001":         {"MTN Relay for RabbitMQ", []string{"MTN Managed Queue", "MTN Managed MQ"}},
	"kaf01":          {"MTN Streamline for Apache Kafka", []string{"MTN Managed Streaming"}},
	"WZH01":          {"MTN Security Monitoring (SIEM)", []string{"MTN SIEM Service"}},
	"bastion987":     {"Warpgate Bastion Host", nil},
	"MVPNSV02":       {"MTN Site-to-Site VPN", []string{"PfSense VPN", "MTN VPN Service", "MTN PfSense VPN"}},
	"WGVPN001":       {"MTN Remote Access VPN", []string{"MTN VPN Server (WireGuard)", "MTN WireGuard SSL VPN"}},
	"K8S":            {"MTN Container Cluster (Self-Managed)", []string{"Kubernetes (MK8S) Engine", "MTN KUBERNETES (MK8S)", "Kubernetes  (MK8S) Engine"}},
	"letsencrypt010": {"MTN SSL Certificates", []string{"Let's Encrypt SSL", "MTN Let's Encrypt SSL"}},
	"FFW03":          {"Fortinet FortiGate Firewall (BYOL)", []string{"MTN Fortinet Firewall (BYOL)"}},
	"FWB003":         {"Fortinet FortiWeb (BYOL)", []string{"MTN FortiWeb (BYOL)"}},
	"FADC003":        {"Fortinet FortiADC (BYOL)", []string{"MTN FortiADC (BYOL)"}},
	"FMB003":         {"Fortinet FortiManager (BYOL)", []string{"MTN FortiManager (BYOL)"}},
	"FAD003":         {"Fortinet FortiAnalyzer (BYOL)", []string{"MTN FortiAnalyzer (BYOL)"}},
	"SPH004":         {"Sophos Firewall (BYOL)", []string{"MTN SOPHOS Firewall (BYOL)"}},
}

// CatalogRef is the reference to the item with this code: its name now, then its former names.
func CatalogRef(code string) CatalogItemRef {
	n := CatalogNames[code]
	return CatalogItemRef{Code: code, Names: append([]string{n.Name}, n.Former...)}
}

// RenamedCatalogItem reports the code and current name of the item that had this (former) name,
// case ignored.
func RenamedCatalogItem(name string) (code, current string, ok bool) {
	for c, n := range CatalogNames {
		for _, f := range n.Former {
			if strings.EqualFold(f, name) {
				return c, n.Name, true
			}
		}
	}
	return "", "", false
}
