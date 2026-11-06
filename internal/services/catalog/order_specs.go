package catalog

import (
	"slices"

	"github.com/mtn/terraform-provider-mtncloud/internal/client"
)

// One typed resource per catalog item (D-026). Field names and widgets come from each item's order
// form (GET /api/catalog/types/{id}, customer login, 2026-10-28); select choices are read live from
// the form at plan time, so no option ID is written here. dev-docs/api-research.md has the shapes.

type kind int

const (
	kText kind = iota
	kNumber
	kFlag
	kSecret      // write-only: sent with the order, never stored
	kList        // textArray: sent comma-separated
	kLines       // textarea: sent comma-separated, as its form asks
	kSelect      // a choice of the form's list, by value or shown name
	kBytes       // given in GB, sent in bytes
	kGroup       // group name: sent as its ID
	kProject     // project name: sent as "pool-<id>"
	kPlan        // plan name: sent as its ID, listed for the project
	kVolumes     // root_volume_size and data_volume blocks (attr is their prefix)
	kNetworks    // network names, the first primary: sent as "network-<id>"
	kSecGroups   // security group names: sent as [{"id": <id>}]
	kFloatingIP  // floating IP pool (external network) name, from the form's list
	kEnvironment // environment name or code: sent as its code
	kLabels      // labels: sent comma-separated
	kExtra       // extra_inputs: other form fields, by field name
)

type field struct {
	attr string
	key  string
	kind kind
	// required: the form requires it. Left out, a field the form pre-fills is sent with the live
	// form's value, as the console does (formFills), so such an argument is optional.
	required bool
	desc     string
}

type orderSpec struct {
	typeName string   // resource type, without the provider prefix
	item     string   // catalog item name, for messages and docs (MTN may rename it)
	code     string   // catalog item code: how the item is found, a rename keeps it
	aliases  []string // former names, for orders placed before a rename
	about    string
	app      bool // the order makes an app (under Apps, not Instances), its VMs "<name>-<n>"
	target   bool // runs against an existing instance (context "instance")
	// reports: the form hides or locks the VM's name, so the console never sends one: the workflow
	// names the VM from MTN's template and prints where it is (its create response
	// `{"instance":{"id":...`, or its console link), which is how the provider finds it
	// (Fortinet/*.sh, PFSense/VPN.sh, Wireguard, Wazuh/wazuh.sh, read 2026-11-02). Such an item
	// has no name argument. A heat item reports too.
	reports bool
	// heat: the workflow names the VM itself, builds it through a Heat stack that a delete leaves,
	// and reports the VM in its output (SOPHOS, `Sophos/sophosheat.sh`; FortiADC,
	// `Fortinet/FortiADCheat.sh`, live 2026-10-31). It takes about an hour.
	heat   bool
	fields []field
	rules  rules
}

// rules are the limits a form states only in its help text (2026-10-30), checked at plan time. Zero
// is no limit.
type rules struct {
	minRootGB, minNetworks, maxNetworks int64
	dataDiskGB                          int64   // a data disk MTN Cloud adds itself (the form's help text)
	minCPU, minMemoryGB                 int64   // from the plan's size code, G<cpu>S<memory>
	badNodes                            []int64 // node counts the form says aren't supported
}

// ref is how the item is found: by its code, then by any name it has had.
func (s orderSpec) ref() client.CatalogItemRef {
	names := append([]string{s.item}, s.aliases...)
	return client.CatalogItemRef{Code: s.code, Names: append(names, formerNames[s.code]...)}
}

// vm is the placement, size, disks and networking a VM item's form asks for. req lists the base
// fields this item's form marks required.
func vm(labelKey string, req ...string) []field {
	r := map[string]bool{}
	for _, k := range req {
		r[k] = true
	}
	return []field{
		{attr: "name", key: "name", kind: kText, required: true,
			desc: "Name of the instance the order creates. It must be unique in the project: the provider finds the instance by it."},
		{attr: "group", key: "group-vpn", kind: kGroup},
		{attr: "project", key: "resourcepool", kind: kProject, required: r["resourcepool"]},
		{attr: "environment", key: "environment", kind: kEnvironment, desc: "Environment, by name or code, for example `Production`."},
		{attr: "labels", key: labelKey, kind: kLabels, desc: "Labels on the instance."},
		{attr: "plan", key: "plan", kind: kPlan, required: r["plan"], desc: "Plan (size), from the order form's own list: its name, or the size code in it (`G2S4` picks `Small (G2S4)`). An unknown plan lists the form's choices."},
		{attr: "", key: "volumes", kind: kVolumes, required: r["volumes"]},
		{attr: "networks", key: "networks", kind: kNetworks, required: r["networks"],
			desc: "MTN Networks in the project, by name or ID. The first is the primary interface. For a network made in " +
				"the same apply, give its ID (`mtncloud_network.x.id`): its name is known at plan, before the network exists."},
		{attr: "security_groups", key: "securitygroups", kind: kSecGroups, required: r["securitygroups"],
			desc: "Security groups in the project, by name, in addition to the project's default group, which is always applied."},
		{attr: "floating_ip_pool", key: "floatingip", kind: kFloatingIP, required: r["floatingip"],
			desc: "External network to take a public (floating) IP from, by name, for example `public-network-01`."},
	}
}

func with(base []field, extra ...field) []field { return append(base, extra...) }

// reported is s for a form that hides or locks the VM's name: no name argument, and the VM is the
// one the workflow reports.
func reported(s orderSpec) orderSpec {
	s.reports = true
	s.fields = without(s.fields, "name")
	return s
}

// without drops the fields with these keys.
func without(base []field, keys ...string) []field {
	out := make([]field, 0, len(base))
	for _, f := range base {
		drop := false
		for _, k := range keys {
			drop = drop || f.key == k
		}
		if !drop {
			out = append(out, f)
		}
	}
	return out
}

// Database fields the MySQL, MariaDB, PostgreSQL, LAMP and LEMP forms share.
func dbFields(versionKey, versionList string, versionRequired bool) []field {
	return []field{
		{attr: "version", key: versionKey, kind: kSelect, required: versionRequired,
			desc: versionList + " version, as the form lists it."},
		{attr: "database_name", key: "dbName", kind: kText, desc: "Database to create."},
		{attr: "database_user", key: "dbUsername", kind: kText, desc: "Database user to create."},
		{attr: "database_password", key: "dbPassword", kind: kSecret, desc: "That user's password."},
		{attr: "user_owns_database", key: "makeUserDbOwner", kind: kFlag, desc: "Make the user the database's owner."},
		{attr: "require_ssl", key: "requireSSL", kind: kFlag, desc: "Require TLS for connections."},
		{attr: "allowed_cidrs", key: "hostCIDRs", kind: kLines,
			desc: "Networks that may connect to the database, as CIDRs."},
		{attr: "use_host_ssl", key: "useHostSSL", kind: kFlag, desc: "Use the host's TLS certificate."},
		{attr: "password_expiry_days", key: "passwordExpiryDays", kind: kNumber, desc: "Days before database passwords expire."},
		{attr: "auto_security_updates", key: "autoSecurityUpdates", kind: kFlag, desc: "Install security updates automatically."},
	}
}

func phpFields(required bool) []field {
	return []field{
		{attr: "php_version", key: "php_version", kind: kSelect, required: required, desc: "PHP version."},
		{attr: "php_memory_limit", key: "php_memory_limit", kind: kSelect, required: required, desc: "PHP memory limit, for example `256M`."},
		{attr: "php_upload_max", key: "php_upload_max", kind: kSelect, required: required, desc: "Largest upload PHP accepts, for example `64M`."},
	}
}

var webPort = field{attr: "web_server_port", key: "webServerPort", kind: kNumber, desc: "Port the web server listens on."}

func adminFields() []field {
	return []field{
		// Required on the form, with a default there (`admin`): left out, the form's default is sent.
		{attr: "admin_username", key: "adminUsername", kind: kText, desc: "Administrator user name. Left out, the form's default (`admin`)."},
		{attr: "admin_password", key: "adminPassword", kind: kSecret, required: true, desc: "Administrator password."},
	}
}

// itemCodes are the codes of the items built with the db and appliance helpers
// (/api/catalog-item-types, 2026-10-30).
var itemCodes = map[string]string{
	"MTN SQL Database - MySQL": "MySQLINST01", "MTN SQL Database · PostgreSQL": "PoSQLINST01", "MTN SQL Database - MariaDB": "MariaDB2378",
	"Fortinet FortiGate Firewall (BYOL)": "FFW03", "Fortinet FortiWeb (BYOL)": "FWB003", "Fortinet FortiADC (BYOL)": "FADC003",
	"Fortinet FortiManager (BYOL)": "FMB003", "Fortinet FortiAnalyzer (BYOL)": "FAD003", "Sophos Firewall (BYOL)": "SPH004",
}

// formerNames are the names an item has had, by code: MTN renamed most of the catalog on 2026-11-06.
// Orders placed before that, and a login that can only find items by name, still match them.
var formerNames = map[string][]string{
	"MySQLINST01":    {"MTN MySQL"},
	"PoSQLINST01":    {"MTN PostgreSQL", "MTN SQL Database - PostgreSQL"},
	"MariaDB2378":    {"MTN MariaDB"},
	"MongoDBINST01":  {"MTN MongoDB"},
	"wordpres010":    {"MTN WordPress"},
	"APACHEWS001":    {"MTN Apache Web Server"},
	"NGINXWS001":     {"MTN Nginx Web Server"},
	"MQS001":         {"MTN Managed MQ"},
	"MVPNSV02":       {"MTN PfSense VPN"},
	"WGVPN001":       {"MTN WireGuard SSL VPN"},
	"SPH004":         {"MTN SOPHOS Firewall (BYOL)"},
	"FFW03":          {"MTN Fortinet Firewall (BYOL)"},
	"FWB003":         {"MTN FortiWeb (BYOL)"},
	"FADC003":        {"MTN FortiADC (BYOL)"},
	"FMB003":         {"MTN FortiManager (BYOL)"},
	"FAD003":         {"MTN FortiAnalyzer (BYOL)"},
	"K8S":            {"MTN KUBERNETES (MK8S)", "Kubernetes  (MK8S) Engine"},
	"letsencrypt010": {"MTN Let's Encrypt SSL"},
}

var applianceBase = []string{"resourcepool", "plan", "networks", "floatingip"}

func orderSpecs() []orderSpec {
	db := func(item, typeName, about, vKey, vList string, vReq bool) orderSpec {
		return orderSpec{typeName: typeName, item: item, code: itemCodes[item], about: about,
			fields: with(vm("labels", "resourcepool", "plan", "volumes", "networks"), dbFields(vKey, vList, vReq)...)}
	}
	// An appliance's disks are optional on its form, which pre-fills a 10 GB root disk, smaller than
	// every minimum its help text states: the size is required here (owner, 2026-10-31). An order
	// placed without one before keeps working: setting it then is recorded, not ordered again.
	// Every appliance form hides and locks the VM's name (live, 2026-11-02): MTN names it.
	appliance := func(item, typeName, about string, r rules) orderSpec {
		req := append(slices.Clone(applianceBase), "volumes")
		return reported(orderSpec{typeName: typeName, item: item, code: itemCodes[item], about: about, fields: vm("label", req...), rules: r})
	}
	heat := func(s orderSpec) orderSpec { s.heat = true; return reported(s) }
	return []orderSpec{
		db("MTN SQL Database - MySQL", "mysql", "A MySQL server on its own VM.", "mysqlVersion", "MySQL", false),
		db("MTN SQL Database · PostgreSQL", "postgresql", "A PostgreSQL server on its own VM.", "pgVersion", "PostgreSQL", false),
		db("MTN SQL Database - MariaDB", "mariadb", "A MariaDB server on its own VM.", "mariadbVersion", "MariaDB", true),
		{typeName: "mongodb", item: "MongoDB Database", code: "MongoDBINST01", about: "A MongoDB server on its own VM.",
			fields: with(vm("labels", "resourcepool", "plan", "volumes", "networks"),
				field{attr: "version", key: "mongodbVersion", kind: kSelect, required: true, desc: "MongoDB version, as the form lists it."},
				field{attr: "database_name", key: "dbName", kind: kText, desc: "Database to create."},
				field{attr: "database_user", key: "dbUsername", kind: kText, desc: "Database user to create."},
				field{attr: "database_password", key: "dbPassword", kind: kSecret, desc: "That user's password."},
				field{attr: "user_role", key: "mongoUserRole", kind: kSelect, desc: "The user's role, for example `Read & Write`."},
				field{attr: "enable_auth", key: "enableAuth", kind: kFlag, desc: "Turn on authentication."},
				field{attr: "admin_username", key: "adminUsername", kind: kText, desc: "Administrator user name. Left out, the form's default (`Admin`)."},
				// Required by the form only while enable_auth is on (requireOnCode), checked at plan time.
				field{attr: "admin_password", key: "adminPassword", kind: kSecret, desc: "Administrator password. Required while `enable_auth` is on."},
				field{attr: "allowed_cidrs", key: "hostCIDRs", kind: kLines, desc: "Networks that may connect, as CIDRs."},
				field{attr: "require_ssl", key: "requireSSL", kind: kFlag, desc: "Require TLS for connections."},
				field{attr: "auto_security_updates", key: "autoSecurityUpdates", kind: kFlag, desc: "Install security updates automatically."},
				field{attr: "replica_set", key: "initReplicaSet", kind: kFlag, desc: "Start a replica set."},
				field{attr: "replica_set_name", key: "replicaSetName", kind: kText, desc: "Replica set name, used when `replica_set` is on. Left out, the form's default (`rs0`)."},
				field{attr: "cache_size_gb", key: "wiredTigerCacheGB", kind: kNumber, desc: "Storage engine cache size in GB; 0 lets MongoDB choose."},
				field{attr: "install_mongosh", key: "installMongosh", kind: kFlag, desc: "Install the MongoDB shell."},
				field{attr: "install_database_tools", key: "installDatabaseTools", kind: kFlag, desc: "Install the database tools."},
				field{attr: "install_atlas_cli", key: "installAtlasCLI", kind: kFlag, desc: "Install the Atlas CLI."},
				field{attr: "install_mcp_server", key: "installMCPServer", kind: kFlag, desc: "Install the MongoDB MCP server."},
				field{attr: "install_mongosync", key: "installMongosync", kind: kFlag, desc: "Install mongosync."},
				field{attr: "install_bi_connector", key: "installBIConnector", kind: kFlag, desc: "Install the BI connector."},
			)},
		{typeName: "lamp_stack", item: "MTN LAMP (PHP)", code: "lamp01", about: "Linux, Apache, MySQL and PHP on one VM.",
			fields: with(with(with(vm("labels", "resourcepool", "plan", "volumes", "networks", "floatingip"), webPort),
				dbFields("mysqlVersion", "MySQL", false)...), phpFields(false)...)},
		{typeName: "lemp_stack", item: "MTN LEMP (PHP)", code: "Lemp01", about: "Linux, Nginx, MySQL and PHP on one VM.",
			fields: with(with(with(vm("label", "resourcepool", "plan", "volumes", "networks", "floatingip"), webPort),
				dbFields("mysqlVersion", "MySQL", false)...), phpFields(false)...)},
		{typeName: "wordpress", item: "MTN Website Hosting (WordPress)", code: "wordpres010", about: "WordPress with its web server and MySQL on one VM.",
			fields: with(with(vm("labels", "resourcepool", "plan", "volumes", "networks", "floatingip"),
				field{attr: "web_server", key: "webserver_type", kind: kSelect, required: true, desc: "`Apache` or `Nginx`."},
				webPort,
				field{attr: "mysql_version", key: "mysqlVersion", kind: kSelect, desc: "MySQL version, as the form lists it."},
				field{attr: "database_name", key: "dbName", kind: kText, required: true, desc: "WordPress database."},
				field{attr: "database_user", key: "dbUsername", kind: kText, required: true, desc: "WordPress database user."},
				field{attr: "database_password", key: "dbPassword", kind: kSecret, required: true, desc: "That user's password."},
				field{attr: "allowed_cidrs", key: "hostCIDRs", kind: kLines, desc: "Networks that may reach the database, as CIDRs."},
				field{attr: "auto_security_updates", key: "autoSecurityUpdates", kind: kFlag, desc: "Install security updates automatically."},
			), phpFields(true)...)},
		{typeName: "apache_web_server", item: "MTN Web Server - Apache", code: "APACHEWS001", about: "An Apache web server on its own VM.",
			fields: with(vm("labels", "resourcepool", "plan", "volumes", "networks"), webPort,
				field{attr: "scale_factor", key: "scalefactor", kind: kNumber, desc: "Number of VMs."})},
		{typeName: "nginx_web_server", item: "MTN Web Server - Nginx", code: "NGINXWS001", about: "An Nginx web server on its own VM.",
			fields: with(vm("labels", "resourcepool", "plan", "volumes", "networks"), webPort,
				field{attr: "scale_factor", key: "scalefactor", kind: kNumber, desc: "Number of VMs."})},
		{typeName: "managed_database", item: "MTN Managed Database", code: "MDBS001", app: true, about: "A managed MySQL or PostgreSQL database service.",
			// The form hides its floating IP (live, 2026-11-02): no public IP, as in the console.
			fields: with(with(without(vm("labels", "resourcepool", "plan", "volumes", "networks", "securitygroups"), "floatingip"),
				field{attr: "engine", key: "db_engine", kind: kSelect, required: true, desc: "`mysql` or `postgres`."},
				field{attr: "mysql_version", key: "mysqlVersion", kind: kSelect, desc: "MySQL version, when `engine` is `mysql`."},
				field{attr: "postgresql_version", key: "pgVersion", kind: kSelect, desc: "PostgreSQL version, when `engine` is `postgres`."},
				field{attr: "node_count", key: "no_nodes", kind: kNumber, desc: "Number of nodes. A 2-node cluster isn't supported."},
			), adminFields()...), rules: rules{badNodes: []int64{2}}},
		{typeName: "managed_mq", item: "MTN Managed Queue", code: "MQS001", app: true, about: "A managed message queue (RabbitMQ) service.",
			// The form hides its floating IP and locks the node count at 1 (live, 2026-11-02): the
			// console sends its own 1, and so does the provider.
			fields: with(with(without(vm("labels", "resourcepool", "plan", "volumes", "networks"), "floatingip"),
				field{attr: "engine", key: "msg_engine", kind: kSelect, desc: "Messaging engine, for example `rabbit`."},
				field{attr: "rabbitmq_version", key: "rabbitmqVersion", kind: kSelect, desc: "RabbitMQ version, when `engine` is `rabbit`."},
			), adminFields()...)},
		{typeName: "managed_streaming", item: "MTN Managed Streaming", code: "kaf01", app: true, about: "A managed event streaming (Kafka) service.",
			fields: with(with(vm("labels", "plan", "volumes", "networks", "securitygroups"),
				field{attr: "kafka_version", key: "kafkaVersion", kind: kSelect, desc: "Kafka version, as the form lists it."},
			), adminFields()...), rules: rules{minRootGB: 100, dataDiskGB: 60}},
		reported(orderSpec{typeName: "siem", item: "MTN SIEM Service", code: "WZH01", about: "A security information and event management (SIEM) server.",
			fields: with(vm("labels", "plan", "volumes", "networks", "securitygroups"),
				field{attr: "version", key: "wazuhVersion", kind: kSelect, required: true, desc: "SIEM version, as the form lists it."},
				field{attr: "archive_events", key: "enableLogall", kind: kSelect, required: true, desc: "Keep every event: `yes` or `no`."},
				field{attr: "admin_password", key: "wazuhAdminPassword", kind: kSecret, required: true, desc: "Administrator password."},
			), rules: rules{minRootGB: 100, dataDiskGB: 60}}),
		{typeName: "bastion_host", item: "Warpgate Bastion Host", code: "bastion987", about: "A bastion host for SSH, HTTPS and database access.",
			fields: with(vm("labels", "plan", "resourcepool", "volumes", "networks", "floatingip"),
				field{attr: "admin_password", key: "warpgateAdminPassword", kind: kSecret, required: true, desc: "Administrator password."})},
		reported(orderSpec{typeName: "pfsense_vpn", item: "PfSense VPN", code: "MVPNSV02", aliases: []string{"MTN VPN Service"},
			about: "A PfSense site-to-site VPN gateway. Its disks are fixed by MTN Cloud (a 50 GB root disk).",
			// The form's disks are hidden and locked: the provider sends the form's own (50 GB root).
			fields: without(vm("label", applianceBase...), "volumes"),
			rules:  rules{minNetworks: 2}}),
		reported(orderSpec{typeName: "wireguard_vpn", item: "MTN VPN Server (WireGuard)", code: "WGVPN001", about: "A WireGuard VPN server.",
			fields: vm("labels", append(applianceBase, "volumes")...)}),
		heat(appliance("Sophos Firewall (BYOL)", "sophos_firewall", "A SOPHOS firewall. Bring your own license.",
			rules{minRootGB: 80, dataDiskGB: 40, minNetworks: 2, minCPU: 2, minMemoryGB: 4})),
		appliance("Fortinet FortiGate Firewall (BYOL)", "fortinet_firewall", "A Fortinet firewall. Bring your own license.",
			rules{minRootGB: 40, minNetworks: 1, maxNetworks: 2}),
		appliance("Fortinet FortiWeb (BYOL)", "fortiweb", "A FortiWeb web application firewall. Bring your own license.",
			rules{minRootGB: 70, minNetworks: 1, maxNetworks: 2, minCPU: 2, minMemoryGB: 8}),
		heat(appliance("Fortinet FortiADC (BYOL)", "fortiadc", "A FortiADC application delivery controller. Bring your own license.",
			rules{minRootGB: 30, dataDiskGB: 30, minNetworks: 1, maxNetworks: 2, minCPU: 4, minMemoryGB: 16})),
		appliance("Fortinet FortiManager (BYOL)", "fortimanager", "A FortiManager. Bring your own license.",
			rules{minRootGB: 500, minNetworks: 1, maxNetworks: 2, minCPU: 4, minMemoryGB: 8}),
		appliance("Fortinet FortiAnalyzer (BYOL)", "fortianalyzer", "A FortiAnalyzer. Bring your own license.",
			rules{minRootGB: 250, dataDiskGB: 250, minNetworks: 1, maxNetworks: 2, minCPU: 4, minMemoryGB: 8}),
		{typeName: "self_managed_kubernetes", item: "Kubernetes (MK8S) Engine", code: "K8S", app: true,
			about: "A self-managed Kubernetes cluster: MTN Cloud builds it, and you run and maintain it.",
			fields: []field{
				{attr: "name", key: "name", kind: kText, required: true, desc: "Cluster name. Its VMs are named after it."},
				{attr: "group", key: "group-vpn", kind: kGroup},
				{attr: "project", key: "resourcepool", kind: kProject, required: true},
				{attr: "environment", key: "environment", kind: kEnvironment, desc: "Environment, by name or code."},
				{attr: "labels", key: "labels", kind: kLabels, desc: "Labels."},
				{attr: "single_node", key: "single_node_cluster", kind: kFlag, desc: "Run everything on one node."},
				{attr: "master_plan", key: "master_plan", kind: kPlan, required: true, desc: "Plan (size) of the master nodes, by name."},
				{attr: "master_", key: "master_volumes", kind: kVolumes, required: true},
				{attr: "master_security_groups", key: "master_securitygroups", kind: kSecGroups, required: true, desc: "Security groups of the master nodes, by name, in addition to the project's default group, which is always applied."},
				{attr: "master_networks", key: "master_network", kind: kNetworks, required: true, desc: "Networks of the master nodes, by name; the first is primary."},
				{attr: "master_floating_ip_pool", key: "master_floatingip", kind: kFloatingIP, desc: "External network for the masters' public IPs, by name."},
				{attr: "master_count", key: "k8s_master_count", kind: kNumber, desc: "Number of master nodes."},
				{attr: "worker_plan", key: "worker_plan", kind: kPlan, desc: "Plan (size) of the worker nodes, by name."},
				{attr: "worker_", key: "worker_volumes", kind: kVolumes},
				{attr: "worker_security_groups", key: "worker_securitygroups", kind: kSecGroups, desc: "Security groups of the worker nodes, by name, in addition to the project's default group, which is always applied."},
				{attr: "worker_networks", key: "worker_network", kind: kNetworks, desc: "Networks of the worker nodes, by name; the first is primary."},
				{attr: "worker_count", key: "k8s_worker_count", kind: kNumber, desc: "Number of worker nodes."},
				{attr: "cni", key: "cni_provider", kind: kSelect, desc: "Network plugin: `flannel` or `calico`."},
				{attr: "api_endpoint", key: "k8s_api_endpoint", kind: kText, desc: "Address the Kubernetes API is published at."},
			}},
		{typeName: "letsencrypt_certificate", item: "Let's Encrypt SSL", code: "letsencrypt010", target: true,
			about: "Installs a free Let's Encrypt TLS certificate on an existing instance. Let's Encrypt checks the domain " +
				"over HTTP: the instance needs a floating IP, port 80 open to the internet in its security group, and the " +
				"domain's DNS record pointing at that IP before the order runs. The plan checks the instance and the record.",
			fields: []field{
				{attr: "domain_name", key: "sslDomainName", kind: kText, required: true, desc: "Domain the certificate is for. It must already point at the instance."},
				{attr: "admin_email", key: "sslAdminEmail", kind: kText, desc: "Email Let's Encrypt sends expiry notices to."},
				{attr: "staging", key: "sslStaging", kind: kFlag, desc: "Use Let's Encrypt's staging service (a test certificate, no rate limits)."},
			}},
	}
}
