package automation

// The task types a customer's console offers that the inherited provider had no resource for
// (dev-docs/audit-2026-10-27.md). Options, their field context and the allowed targets come from a
// customer's GET /api/task-types (2026-10-27); body shapes from `tasks add -d` dry runs (2026-10-28).

type optKind int

const (
	text     optKind = iota
	secret           // sent, never read back: the API doesn't return it
	flag             // a checkbox; the API may return true or "on"
	headers          // a key-value input, sent as a JSON object string
	ref              // a name people give, sent as the ID of the matching /api/options/<source> entry
	workflow         // a workflow by name: sent as both <key>Name and <key>Id
)

type taskOption struct {
	attr     string
	key      string // option key in the body
	config   bool   // field context "config" (plugin options), else "taskOptions"
	kind     optKind
	required bool
	platform bool   // the platform fills it in when left out: Optional+Computed, kept from state
	source   string // for ref and workflow: the /api/options source
	what     string // for ref and workflow: what it is, for errors
	desc     string
}

type taskSpec struct {
	typeName   string // resource type, without the provider prefix
	code       string // task type code
	title      string // console name
	about      string
	targets    []string // allowed execute targets; one means it isn't an attribute
	results    bool     // the type returns results: result_type applies
	credential bool     // takes a stored credential instead of its own user and password
	options    []taskOption
}

func taskSpecs() []taskSpec {
	return []taskSpec{
		{
			typeName: "ai_task", code: "ai", title: "AI Task", targets: []string{"local"}, results: true,
			about: "Asks one of your AI agents a prompt; the answer is the task's result.",
			options: []taskOption{
				{attr: "ai_agent", key: "aiAgent", kind: ref, required: true, source: "aiAgents", what: "AI agent",
					desc: "The AI agent to ask, by name."},
				{attr: "prompt", key: "aiPrompt", desc: "The prompt. Workflow variables such as `<%=instance.name%>` work."},
			},
		},
		{
			typeName: "conditional_workflow_task", code: "conditionalWorkflow", title: "Conditional Workflow", targets: []string{"local"},
			about: "Runs one operational workflow or another, depending on a JavaScript condition.",
			options: []taskOption{
				{attr: "condition", key: "conditionalScript", desc: "JavaScript that returns true or false. True runs `if_workflow`, false `else_workflow`."},
				{attr: "if_workflow", key: "ifOperationalWorkflow", kind: workflow, required: true, source: "operationWorkflows", what: "operational workflow",
					desc: "The operational workflow run when the condition is true, by name."},
				{attr: "else_workflow", key: "elseOperationalWorkflow", kind: workflow, source: "operationWorkflows", what: "operational workflow",
					desc: "The operational workflow run when the condition is false, by name. Leave it out to do nothing."},
			},
		},
		{
			typeName: "dynamic_wallet_collection_task", code: "dynamic-wallet-collection-task", title: "Dynamic Wallet Collection",
			targets: []string{"resource", "local"}, results: true,
			about: "Runs MTN's dynamic wallet collection. It has no options of its own.",
		},
		{
			typeName: "http_task", code: "httpTask", title: "HTTP", targets: []string{"local"}, results: true, credential: true,
			about: "Calls an HTTP endpoint; the response is the task's result.",
			options: []taskOption{
				{attr: "url", key: "webUrl", required: true, desc: "The URL to call, for example `https://api.example.com/hooks/deploy`."},
				{attr: "method", key: "webMethod", platform: true, desc: "HTTP method, for example `GET` or `POST`. The platform's default is `GET`."},
				{attr: "body", key: "webBody", desc: "Request body."},
				{attr: "headers", key: "httpHeaders", kind: headers, desc: "Request headers, for example `{ \"Content-Type\" = \"application/json\" }`."},
				{attr: "ignore_ssl", key: "ignoreSSL", kind: flag, desc: "Don't verify the endpoint's TLS certificate."},
				{attr: "user", key: "webUser", desc: "User for basic authentication. Leave it out when `credential` is set."},
				{attr: "password", key: "webPassword", kind: secret, desc: "Password for basic authentication. Stored in state (sensitive); not read back, so a change made in the console isn't seen."},
			},
		},
		{
			typeName: "powerscale_storage_collection_task", code: "unotelos-storage-collection-task", title: "PowerScale Storage Collection",
			targets: []string{"local"}, results: true,
			about: "Collects PowerScale storage usage for billing.",
			options: []taskOption{
				{attr: "dry_run", key: "dryRun", config: true, kind: flag, desc: "Collect without sending usage to billing."},
			},
		},
		{
			typeName: "puppet_agent_install_task", code: "puppetTask", title: "Puppet Agent Install", targets: []string{"resource"},
			about: "Installs the Puppet agent on the instance the task runs against.",
			options: []taskOption{
				{attr: "puppet_master", key: "puppetMasterId", kind: ref, required: true, source: "puppet", what: "Puppet master",
					desc: "The Puppet master integration, by name."},
				{attr: "puppet_version", key: "puppetVersion", required: true, desc: "Agent version, as the console lists it, for example `7`."},
				{attr: "node_name", key: "puppetNodeName", platform: true, desc: "Puppet node name. The platform's default is the instance's host name."},
				{attr: "environment", key: "puppetEnvironment", desc: "Puppet environment, for example `production`."},
			},
		},
		{
			typeName: "reverse_text_task", code: "reverseTextTask", title: "Reverse Text", targets: []string{"resource", "local"}, results: true,
			about: "Returns its text reversed. Useful to try out workflows.",
			options: []taskOption{
				{attr: "text", key: "reversibleText", config: true, required: true, desc: "The text to reverse."},
			},
		},
	}
}
