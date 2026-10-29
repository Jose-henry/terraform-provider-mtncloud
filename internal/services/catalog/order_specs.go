package catalog

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
	kLines       // textarea: sent one per line
	kSelect      // a choice of the form's list, by value or shown name
	kBytes       // given in GB, sent in bytes
	kGroup       // group name: sent as its ID
	kProject     // project name: sent as "pool-<id>"
	kPlan        // plan name: sent as its ID, listed for the project
	kVolumes     // root_volume_size, datastore and data_volume blocks (attr is their prefix)
	kNetworks    // network names, the first primary: sent as "network-<id>"
	kSecGroups   // security group names: sent as [{"id": <id>}]
	kFloatingIP  // floating IP pool (external network) name, from the form's list
	kEnvironment // environment name or code: sent as its code
	kLabels      // labels: sent comma-separated
)

type field struct {
	attr     string
	key      string
	kind     kind
	required bool
	def      string // sent when left out, as the console would
	desc     string
}

type orderSpec struct {
	typeName string // resource type, without the provider prefix
	item     string // catalog item name
	about    string
	app      bool // the order makes an app (under Apps, not Instances), its VMs "<name>-<n>"
	target   bool // runs against an existing instance (context "instance")
	fields   []field
	// movedFrom is a former type name. The old name stays as a deprecated alias, and a `moved {}`
	// block moves its state here without a replace.
	movedFrom  string
	deprecated string // set on the alias: the type name that replaces it
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
		{attr: "plan", key: "plan", kind: kPlan, required: r["plan"], desc: "Plan (size), by name, for example `G2S4`."},
		{attr: "", key: "volumes", kind: kVolumes, required: r["volumes"]},
		{attr: "networks", key: "networks", kind: kNetworks, required: r["networks"],
			desc: "MTN Networks in the project, by name. The first is the primary interface."},
		{attr: "security_groups", key: "securitygroups", kind: kSecGroups, required: r["securitygroups"],
			desc: "Security groups in the project, by name."},
		{attr: "floating_ip_pool", key: "floatingip", kind: kFloatingIP, required: r["floatingip"],
			desc: "External network to take a public (floating) IP from, by name, for example `public-network-01`."},
	}
}

func with(base []field, extra ...field) []field { return append(base, extra...) }

// Database fields the MySQL, MariaDB, PostgreSQL, LAMP and LEMP forms share.
func dbFields(versionKey, versionList, versionDef string, versionRequired bool) []field {
	return []field{
		{attr: "version", key: versionKey, kind: kSelect, required: versionRequired, def: versionDef,
			desc: versionList + " version, as the form lists it."},
		{attr: "database_name", key: "dbName", kind: kText, desc: "Database to create."},
		{attr: "database_user", key: "dbUsername", kind: kText, desc: "Database user to create."},
		{attr: "database_password", key: "dbPassword", kind: kSecret, desc: "That user's password."},
		{attr: "user_owns_database", key: "makeUserDbOwner", kind: kFlag, def: "false", desc: "Make the user the database's owner."},
		{attr: "require_ssl", key: "requireSSL", kind: kFlag, desc: "Require TLS for connections."},
		{attr: "allowed_cidrs", key: "hostCIDRs", kind: kLines, def: "0.0.0.0/0",
			desc: "Networks that may connect to the database, as CIDRs. Defaults to `[\"0.0.0.0/0\"]`."},
		{attr: "use_host_ssl", key: "useHostSSL", kind: kFlag, desc: "Use the host's TLS certificate."},
		{attr: "password_expiry_days", key: "passwordExpiryDays", kind: kNumber, desc: "Days before database passwords expire."},
		{attr: "auto_security_updates", key: "autoSecurityUpdates", kind: kFlag, desc: "Install security updates automatically."},
	}
}

func phpFields(versionDef, memoryDef, uploadDef string, required bool) []field {
	return []field{
		// A required choice with a default is optional here: the default is sent, as the console does.
		{attr: "php_version", key: "php_version", kind: kSelect, required: required && versionDef == "", def: versionDef, desc: "PHP version."},
		{attr: "php_memory_limit", key: "php_memory_limit", kind: kSelect, required: required && memoryDef == "", def: memoryDef, desc: "PHP memory limit, for example `256M`."},
		{attr: "php_upload_max", key: "php_upload_max", kind: kSelect, required: required && uploadDef == "", def: uploadDef, desc: "Largest upload PHP accepts, for example `64M`."},
	}
}

var webPort = field{attr: "web_server_port", key: "webServerPort", kind: kNumber, desc: "Port the web server listens on."}

func adminFields() []field {
	return []field{
		{attr: "admin_username", key: "adminUsername", kind: kText, required: true, desc: "Administrator user name."},
		{attr: "admin_password", key: "adminPassword", kind: kSecret, required: true, desc: "Administrator password."},
	}
}

var applianceBase = []string{"resourcepool", "plan", "networks", "floatingip"}

func orderSpecs() []orderSpec {
	db := func(item, typeName, about, vKey, vList, vDef string, vReq bool) orderSpec {
		return orderSpec{typeName: typeName, item: item, about: about,
			fields: with(vm("labels", "resourcepool", "plan", "volumes", "networks"), dbFields(vKey, vList, vDef, vReq)...)}
	}
	appliance := func(item, typeName, about string) orderSpec {
		return orderSpec{typeName: typeName, item: item, about: about, fields: vm("label", applianceBase...)}
	}
	return []orderSpec{
		db("MTN MySQL", "mysql", "A MySQL server on its own VM.", "mysqlVersion", "MySQL", "", false),
		db("MTN PostgreSQL", "postgresql", "A PostgreSQL server on its own VM.", "pgVersion", "PostgreSQL", "17", false),
		db("MTN MariaDB", "mariadb", "A MariaDB server on its own VM.", "mariadbVersion", "MariaDB", "11.8", true),
		{typeName: "mongodb", item: "MTN MongoDB", about: "A MongoDB server on its own VM.",
			fields: with(vm("labels", "resourcepool", "plan", "volumes", "networks"),
				field{attr: "version", key: "mongodbVersion", kind: kSelect, required: true, def: "8.2", desc: "MongoDB version, as the form lists it."},
				field{attr: "database_name", key: "dbName", kind: kText, desc: "Database to create."},
				field{attr: "database_user", key: "dbUsername", kind: kText, desc: "Database user to create."},
				field{attr: "database_password", key: "dbPassword", kind: kSecret, desc: "That user's password."},
				field{attr: "user_role", key: "mongoUserRole", kind: kSelect, def: "Read & Write", desc: "The user's role, for example `Read & Write`."},
				field{attr: "enable_auth", key: "enableAuth", kind: kFlag, def: "true", desc: "Turn on authentication. Defaults to true."},
				field{attr: "admin_username", key: "adminUsername", kind: kText, required: true, desc: "Administrator user name."},
				field{attr: "admin_password", key: "adminPassword", kind: kSecret, required: true, desc: "Administrator password."},
				field{attr: "allowed_cidrs", key: "hostCIDRs", kind: kLines, def: "0.0.0.0/0", desc: "Networks that may connect, as CIDRs. Defaults to `[\"0.0.0.0/0\"]`."},
				field{attr: "require_ssl", key: "requireSSL", kind: kFlag, desc: "Require TLS for connections."},
				field{attr: "auto_security_updates", key: "autoSecurityUpdates", kind: kFlag, desc: "Install security updates automatically."},
				field{attr: "replica_set", key: "initReplicaSet", kind: kFlag, desc: "Start a replica set."},
				field{attr: "replica_set_name", key: "replicaSetName", kind: kText, def: "rs0", desc: "Replica set name. Defaults to `rs0`."},
				field{attr: "cache_size_gb", key: "wiredTigerCacheGB", kind: kNumber, desc: "Storage engine cache size in GB; 0 lets MongoDB choose."},
				field{attr: "install_mongosh", key: "installMongosh", kind: kFlag, def: "true", desc: "Install the MongoDB shell. Defaults to true."},
				field{attr: "install_database_tools", key: "installDatabaseTools", kind: kFlag, def: "true", desc: "Install the database tools. Defaults to true."},
				field{attr: "install_atlas_cli", key: "installAtlasCLI", kind: kFlag, desc: "Install the Atlas CLI."},
				field{attr: "install_mcp_server", key: "installMCPServer", kind: kFlag, desc: "Install the MongoDB MCP server."},
				field{attr: "install_mongosync", key: "installMongosync", kind: kFlag, desc: "Install mongosync."},
				field{attr: "install_bi_connector", key: "installBIConnector", kind: kFlag, desc: "Install the BI connector."},
			)},
		{typeName: "lamp_stack", item: "MTN LAMP (PHP)", about: "Linux, Apache, MySQL and PHP on one VM.",
			fields: with(with(with(vm("labels", "resourcepool", "plan", "volumes", "networks", "floatingip"), webPort),
				dbFields("mysqlVersion", "MySQL", "", false)...), phpFields("8.4", "256M", "64M", false)...)},
		{typeName: "lemp_stack", item: "MTN LEMP (PHP)", about: "Linux, Nginx, MySQL and PHP on one VM.",
			fields: with(with(with(vm("label", "resourcepool", "plan", "volumes", "networks", "floatingip"), webPort),
				dbFields("mysqlVersion", "MySQL", "8.0", false)...), phpFields("8.4", "256M", "64M", false)...)},
		{typeName: "wordpress", item: "MTN WordPress", about: "WordPress with its web server and MySQL on one VM.",
			fields: with(with(vm("labels", "resourcepool", "plan", "volumes", "networks", "floatingip"),
				field{attr: "web_server", key: "webserver_type", kind: kSelect, required: true, desc: "`Apache` or `Nginx`."},
				webPort,
				field{attr: "mysql_version", key: "mysqlVersion", kind: kSelect, desc: "MySQL version, as the form lists it."},
				field{attr: "database_name", key: "dbName", kind: kText, required: true, desc: "WordPress database."},
				field{attr: "database_user", key: "dbUsername", kind: kText, required: true, desc: "WordPress database user."},
				field{attr: "database_password", key: "dbPassword", kind: kSecret, required: true, desc: "That user's password."},
				field{attr: "user_owns_database", key: "makeUserDbOwner", kind: kFlag, def: "true", desc: "Make the user the database's owner. Defaults to true."},
				field{attr: "allowed_cidrs", key: "hostCIDRs", kind: kLines, def: "0.0.0.0/0", desc: "Networks that may reach the database, as CIDRs. Defaults to `[\"0.0.0.0/0\"]`."},
				field{attr: "auto_security_updates", key: "autoSecurityUpdates", kind: kFlag, desc: "Install security updates automatically."},
			), phpFields("8.3 (Recommended)", "512M (Recommended)", "", true)...)},
		{typeName: "apache_web_server", item: "MTN Apache Web Server", about: "An Apache web server on its own VM.",
			fields: with(vm("labels", "resourcepool", "plan", "volumes", "networks"), webPort,
				field{attr: "scale_factor", key: "scalefactor", kind: kNumber, def: "1", desc: "Number of VMs. Defaults to 1."})},
		{typeName: "nginx_web_server", item: "MTN Nginx Web Server", about: "An Nginx web server on its own VM.",
			fields: with(vm("labels", "resourcepool", "plan", "volumes", "networks"), webPort,
				field{attr: "scale_factor", key: "scalefactor", kind: kNumber, def: "1", desc: "Number of VMs. Defaults to 1."})},
		{typeName: "managed_database", item: "MTN Managed Database", app: true, about: "A managed MySQL or PostgreSQL database service.",
			fields: with(with(vm("labels", "resourcepool", "plan", "volumes", "networks", "securitygroups"),
				field{attr: "engine", key: "db_engine", kind: kSelect, required: true, desc: "`mysql` or `postgres`."},
				field{attr: "mysql_version", key: "mysqlVersion", kind: kSelect, desc: "MySQL version, when `engine` is `mysql`."},
				field{attr: "postgresql_version", key: "pgVersion", kind: kSelect, desc: "PostgreSQL version, when `engine` is `postgres`."},
				field{attr: "node_count", key: "no_nodes", kind: kNumber, def: "1", desc: "Number of nodes. Defaults to 1."},
			), adminFields()...)},
		{typeName: "managed_mq", item: "MTN Managed MQ", app: true, about: "A managed message queue (RabbitMQ) service.",
			fields: with(with(vm("labels", "resourcepool", "plan", "volumes", "networks"),
				field{attr: "engine", key: "msg_engine", kind: kSelect, desc: "Messaging engine, for example `rabbit`."},
				field{attr: "rabbitmq_version", key: "rabbitmqVersion", kind: kSelect, desc: "RabbitMQ version, when `engine` is `rabbit`."},
				field{attr: "node_count", key: "no_nodes", kind: kNumber, required: true, def: "1", desc: "Number of nodes."},
			), adminFields()...)},
		{typeName: "managed_streaming", item: "MTN Managed Streaming", app: true, about: "A managed event streaming (Kafka) service.",
			fields: with(with(vm("labels", "plan", "volumes", "networks", "securitygroups"),
				field{attr: "kafka_version", key: "kafkaVersion", kind: kSelect, desc: "Kafka version, as the form lists it."},
			), adminFields()...)},
		{typeName: "siem", item: "MTN SIEM Service", about: "A security information and event management (SIEM) server.",
			fields: with(vm("labels", "plan", "volumes", "networks", "securitygroups"),
				field{attr: "version", key: "wazuhVersion", kind: kSelect, required: true, desc: "SIEM version, as the form lists it."},
				field{attr: "archive_events", key: "enableLogall", kind: kSelect, required: true, desc: "Keep every event, `Yes` or `No`."},
				field{attr: "admin_password", key: "wazuhAdminPassword", kind: kSecret, required: true, desc: "Administrator password."},
			)},
		{typeName: "bastion_host", item: "Warpgate Bastion Host", about: "A bastion host for SSH, HTTPS and database access.",
			fields: with(vm("labels", "plan", "resourcepool", "networks", "floatingip"),
				field{attr: "admin_password", key: "warpgateAdminPassword", kind: kSecret, required: true, desc: "Administrator password."})},
		{typeName: "vpn_service", item: "MTN VPN Service", about: "A site-to-site VPN gateway.",
			fields: with(vm("label", applianceBase...),
				field{attr: "data_volume_size", key: "volume", kind: kBytes, def: "50", desc: "Data disk size in GB. Defaults to 50."})},
		appliance("MTN WireGuard SSL VPN", "wireguard_vpn", "A WireGuard VPN server."),
		appliance("MTN Fortinet Firewall (BYOL)", "fortinet_firewall", "A Fortinet firewall. Bring your own license."),
		appliance("MTN FortiWeb (BYOL)", "fortiweb", "A FortiWeb web application firewall. Bring your own license."),
		appliance("MTN FortiADC (BYOL)", "fortiadc", "A FortiADC application delivery controller. Bring your own license."),
		appliance("MTN FortiManager (BYOL)", "fortimanager", "A FortiManager. Bring your own license."),
		appliance("MTN FortiAnalyzer (BYOL)", "fortianalyzer", "A FortiAnalyzer. Bring your own license."),
		{typeName: "self_managed_kubernetes", movedFrom: "managed_kubernetes", item: "MTN KUBERNETES (MK8S)", app: true,
			about: "A self-managed Kubernetes cluster: MTN Cloud builds it, and you run and maintain it.",
			fields: []field{
				{attr: "name", key: "name", kind: kText, required: true, desc: "Cluster name. Its VMs are named after it."},
				{attr: "group", key: "group-vpn", kind: kGroup},
				{attr: "project", key: "resourcepool", kind: kProject, required: true},
				{attr: "environment", key: "environment", kind: kEnvironment, desc: "Environment, by name or code."},
				{attr: "labels", key: "labels", kind: kLabels, desc: "Labels."},
				{attr: "single_node", key: "single_node_cluster", kind: kFlag, def: "false", desc: "Run everything on one node."},
				{attr: "master_plan", key: "master_plan", kind: kPlan, required: true, desc: "Plan (size) of the master nodes, by name."},
				{attr: "master_", key: "master_volumes", kind: kVolumes, required: true},
				{attr: "master_security_groups", key: "master_securitygroups", kind: kSecGroups, required: true, desc: "Security groups of the master nodes, by name."},
				{attr: "master_networks", key: "master_network", kind: kNetworks, required: true, desc: "Networks of the master nodes, by name; the first is primary."},
				{attr: "master_floating_ip_pool", key: "master_floatingip", kind: kFloatingIP, desc: "External network for the masters' public IPs, by name."},
				{attr: "master_count", key: "k8s_master_count", kind: kNumber, def: "1", desc: "Number of master nodes. Defaults to 1."},
				{attr: "worker_plan", key: "worker_plan", kind: kPlan, desc: "Plan (size) of the worker nodes, by name."},
				{attr: "worker_", key: "worker_volumes", kind: kVolumes},
				{attr: "worker_security_groups", key: "worker_securitygroups", kind: kSecGroups, desc: "Security groups of the worker nodes, by name."},
				{attr: "worker_networks", key: "worker_network", kind: kNetworks, desc: "Networks of the worker nodes, by name; the first is primary."},
				{attr: "worker_floating_ip_pool", key: "worker_floatingip", kind: kFloatingIP, desc: "External network for the workers' public IPs, by name."},
				{attr: "worker_count", key: "k8s_worker_count", kind: kNumber, def: "3", desc: "Number of worker nodes. Defaults to 3."},
				{attr: "cni", key: "cni_provider", kind: kSelect, desc: "Network plugin: `flannel` or `calico`."},
				{attr: "api_endpoint", key: "k8s_api_endpoint", kind: kText, desc: "Address the Kubernetes API is published at."},
			}},
		{typeName: "letsencrypt_certificate", item: "MTN Let's Encrypt SSL", target: true,
			about: "Installs a free Let's Encrypt TLS certificate on an existing instance.",
			fields: []field{
				{attr: "domain_name", key: "sslDomainName", kind: kText, required: true, desc: "Domain the certificate is for. It must already point at the instance."},
				{attr: "admin_email", key: "sslAdminEmail", kind: kText, desc: "Email Let's Encrypt sends expiry notices to."},
				{attr: "staging", key: "sslStaging", kind: kFlag, desc: "Use Let's Encrypt's staging service (a test certificate, no rate limits)."},
			}},
	}
}
