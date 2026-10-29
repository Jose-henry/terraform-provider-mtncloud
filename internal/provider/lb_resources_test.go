package provider

import (
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/knownvalue"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/statecheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"
)

// lbPlugin is a stateful fake of the load balancer task's write actions, answering the way the live
// plugin did on 2026-10-29: create answers PENDING_CREATE, piece writes answer steps[], delete
// answers {deleting: true}, and the load balancer reads PENDING_UPDATE once after each write.
type lbPlugin struct {
	mu       sync.Mutex
	n        int
	lbs      map[string]map[string]any
	pending  map[string]bool
	jobs     map[int64][]byte
	runs     map[string]int            // action -> runs
	sent     map[string]map[string]any // action -> the last spec it was sent
	released []string                  // fip-detach's release, per detach
}

func newLBPlugin() *lbPlugin {
	return &lbPlugin{lbs: map[string]map[string]any{}, pending: map[string]bool{}, jobs: map[int64][]byte{}, runs: map[string]int{}, sent: map[string]map[string]any{}}
}

func (p *lbPlugin) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.URL.Path == "/api/tasks":
		_, _ = w.Write([]byte(`{"tasks":[{"id":450,"code":"lboperate.terraform"}],"meta":{"total":1}}`))
	case r.URL.Path == "/api/networks/114":
		_, _ = w.Write([]byte(`{"network":{"id":114,"name":"app","subnets":[{"id":82,"cidr":"10.0.0.0/24"}]}}`))
	case r.URL.Path == "/api/subnets/82":
		_, _ = w.Write([]byte(`{"subnet":{"id":82,"externalId":"sub-82"}}`))
	case r.Method == http.MethodPost && r.URL.Path == "/api/tasks/450/execute":
		var body struct {
			Job struct {
				CustomOptions map[string]any `json:"customOptions"`
			} `json:"job"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		p.mu.Lock()
		p.n++
		id := int64(40000 + p.n)
		env := p.run(body.Job.CustomOptions)
		out, _ := json.Marshal(env)
		status := "success"
		if env["success"] != true {
			status = "error"
		}
		p.jobs[id], _ = json.Marshal(map[string]any{"jobExecution": map[string]any{"id": id, "status": status, "process": map[string]any{
			"events": []any{map[string]any{"processType": map[string]any{"code": "executeTask"}, "output": string(out)}}}}})
		p.mu.Unlock()
		_, _ = fmt.Fprintf(w, `{"success":true,"jobExecution":{"id":%d}}`, id)
	case strings.HasPrefix(r.URL.Path, "/api/job-executions/"):
		var id int64
		_, _ = fmt.Sscan(strings.TrimPrefix(r.URL.Path, "/api/job-executions/"), &id)
		p.mu.Lock()
		b := p.jobs[id]
		p.mu.Unlock()
		_, _ = w.Write(b)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func (p *lbPlugin) id(prefix string) string { p.n++; return fmt.Sprintf("%s-%d", prefix, p.n) }

func list(m map[string]any, k string) []any { l, _ := m[k].([]any); return l }

func find(l []any, id string) (map[string]any, int) {
	for i, x := range l {
		if m := x.(map[string]any); m["id"] == id {
			return m, i
		}
	}
	return nil, -1
}

// apply copies the spec fields a piece action sends onto the stored piece, under their read names.
func apply(piece, spec map[string]any, names map[string]string) {
	for k, v := range spec {
		if n, ok := names[k]; ok {
			piece[n] = v
		}
	}
}

var (
	listenerNames = map[string]string{"name": "name", "description": "description", "protocol": "protocol", "port": "protocol_port",
		"defaultPoolId": "default_pool_id", "connectionLimit": "connection_limit", "allowedCidrs": "allowed_cidrs", "adminStateUp": "admin_state_up",
		"defaultTlsContainerRef": "default_tls_container_ref", "sniContainerRefs": "sni_container_refs", "timeoutClientData": "timeout_client_data",
		"timeoutMemberConnect": "timeout_member_connect", "timeoutMemberData": "timeout_member_data", "timeoutTcpInspect": "timeout_tcp_inspect"}
	poolNames    = map[string]string{"name": "name", "description": "description", "protocol": "protocol", "algorithm": "lb_algorithm", "tlsEnabled": "tls_enabled", "adminStateUp": "admin_state_up"}
	monitorNames = map[string]string{"name": "name", "type": "type", "delay": "delay", "timeout": "timeout", "maxRetries": "max_retries",
		"maxRetriesDown": "max_retries_down", "httpMethod": "http_method", "urlPath": "url_path", "expectedCodes": "expected_codes", "adminStateUp": "admin_state_up"}
	memberNames = map[string]string{"name": "name", "address": "address", "port": "protocol_port", "subnetId": "subnet_id", "weight": "weight",
		"backup": "backup", "monitorAddress": "monitor_address", "monitorPort": "monitor_port", "adminStateUp": "admin_state_up"}
)

func (p *lbPlugin) run(o map[string]any) map[string]any {
	action, _ := o["action"].(string)
	p.runs[action]++
	ok := func(result any) map[string]any {
		return map[string]any{"success": true, "action": action, "result": result}
	}
	refuse := func(f string, a ...any) map[string]any {
		return map[string]any{"success": false, "action": action, "error": fmt.Sprintf(f, a...)}
	}
	// made answers a piece create as the plugin does since f577a90: its step, and the piece itself.
	made := func(kind string, piece map[string]any) map[string]any {
		out := ok(map[string]any{"success": true, "steps": []any{map[string]any{"kind": kind, "name": "", "applied": true, "created": true, "id": piece["id"]}}})
		out["piece"] = piece
		return out
	}
	if o["networkId"] != "114" {
		return refuse("Network %v is not available to your tenant.", o["networkId"])
	}
	spec, _ := o["spec"].(map[string]any)
	p.sent[action] = maps.Clone(spec)
	if action == "create" {
		req := "mtncloud-req-" + fmt.Sprint(o["requestId"])
		for _, lb := range p.lbs {
			for _, t := range list(lb, "tags") {
				if t == req {
					return ok(map[string]any{"success": true, "existing": true, "loadbalancer": lb})
				}
			}
		}
		lb := map[string]any{"id": p.id("lb"), "name": spec["name"], "description": spec["description"], "vip_subnet_id": spec["vipSubnetId"],
			"vip_address": "10.0.0.20", "provisioning_status": "PENDING_CREATE", "operating_status": "OFFLINE", "admin_state_up": true,
			"tags": append(append([]any{}, list(spec, "tags")...), req), "listeners": []any{}, "pools": []any{}}
		if a, has := spec["vipAddress"]; has {
			lb["vip_address"] = a
		}
		p.lbs[lb["id"].(string)] = lb
		answer := map[string]any{}
		for k, v := range lb {
			answer[k] = v
		}
		lb["provisioning_status"] = "ACTIVE"
		p.pending[lb["id"].(string)] = true
		return ok(map[string]any{"success": true, "loadbalancer": answer})
	}
	switch action {
	case "list":
		rows := []any{}
		for _, x := range p.lbs {
			fip, _ := x["fip"].(map[string]any)
			addr := any(nil)
			if fip != nil {
				addr = fip["floating_ip_address"]
			}
			rows = append(rows, map[string]any{"id": x["id"], "name": x["name"], "provisioning_status": "ACTIVE", "operating_status": "ONLINE",
				"vip_floating": addr, "vip_floating_known": true})
		}
		return ok(map[string]any{"success": true, "loadbalancers": rows, "total": len(rows), "canManage": true})
	case "fip-options":
		return ok(map[string]any{"success": true,
			"floatingIpPools":      []any{map[string]any{"name": "public-network-02", "externalId": "ext-02"}},
			"availableFloatingIps": []any{map[string]any{"id": "fip-held", "address": "196.0.0.9"}}})
	}
	lbID, _ := o["lbId"].(string)
	lb := p.lbs[lbID]
	if lb == nil {
		return refuse("Load balancer %s was not found on this network.", lbID)
	}
	if action == "read" {
		out := map[string]any{}
		for k, v := range lb {
			out[k] = v
		}
		if p.pending[lbID] {
			out["provisioning_status"], p.pending[lbID] = "PENDING_UPDATE", false
		}
		return ok(map[string]any{"success": true, "canManage": true, "loadbalancer": out})
	}
	if action == "delete" {
		delete(p.lbs, lbID)
		return ok(map[string]any{"success": true, "id": lbID, "deleting": true})
	}
	p.pending[lbID] = true
	pools := list(lb, "pools")
	var tag string
	switch action {
	case "listener-create", "pool-create", "member-add", "monitor-create", "l7policy-create":
		req, _ := o["requestId"].(string)
		if !requestIDForm.MatchString(req) {
			return refuse("requestId must be 1 to 64 of A-Za-z0-9_-, not %q.", req)
		}
		tag = "mtncloud-req-" + req
		if x := taggedPiece(lb, tag); x != nil { // a retry: nothing changes
			out := ok(map[string]any{"success": true, "steps": []any{}})
			out["existing"], out["piece"] = true, x
			return out
		}
	}
	pool, _ := find(pools, fmt.Sprint(o["poolId"]))
	needPool := func() map[string]any {
		if pool == nil {
			return refuse("Pool %v is not on this load balancer.", o["poolId"])
		}
		return nil
	}
	switch action {
	case "loadbalancer-update":
		for k, v := range spec {
			switch k {
			case "name", "description":
				if v != nil {
					lb[k] = v
				}
			case "adminStateUp":
				if v != nil {
					lb["admin_state_up"] = v
				}
			case "tags":
				if v == nil {
					continue
				}
				kept := []any{}
				for _, t := range list(lb, "tags") {
					if strings.HasPrefix(fmt.Sprint(t), "mtncloud-req-") {
						kept = append(kept, t)
					}
				}
				lb["tags"] = append(kept, v.([]any)...)
			default:
				return refuse("Only name, description, tags and adminStateUp can be changed this way, not %s.", k)
			}
		}
		return ok(map[string]any{"success": true, "piece": map[string]any{"id": lbID, "name": lb["name"]}})
	case "listener-create":
		l := map[string]any{"id": p.id("l"), "name": "", "description": "", "connection_limit": -1, "allowed_cidrs": []any{}, "admin_state_up": true,
			"default_pool_id": nil, "timeout_client_data": 50000, "timeout_member_connect": 5000, "timeout_member_data": 50000, "timeout_tcp_inspect": 0}
		apply(l, spec, listenerNames)
		l["tags"] = []any{tag}
		lb["listeners"] = append(list(lb, "listeners"), l)
		return made("listener", l)
	case "listener-update", "listener-delete":
		l, i := find(list(lb, "listeners"), fmt.Sprint(o["listenerId"]))
		if l == nil {
			return refuse("Listener %v is not on this load balancer.", o["listenerId"])
		}
		if action == "listener-delete" {
			lb["listeners"] = append(list(lb, "listeners")[:i], list(lb, "listeners")[i+1:]...)
			return ok(map[string]any{"success": true, "steps": []any{}})
		}
		delete(spec, "protocol")
		delete(spec, "port")
		if v, has := spec["defaultTlsContainerRef"]; has && v == nil && l["protocol"] == "TERMINATED_HTTPS" {
			return refuse("Listener %v terminates TLS, so it must keep a certificate. Point it at another certificate, or replace the listener.", o["listenerId"])
		}
		apply(l, spec, listenerNames)
		return ok(map[string]any{"success": true, "steps": []any{}})
	case "pool-create":
		pl := map[string]any{"id": p.id("pool"), "name": "", "description": "", "tls_enabled": false, "admin_state_up": true, "members": []any{}, "healthmonitor": nil}
		apply(pl, spec, poolNames)
		setPersistence(pl, spec)
		pl["tags"] = []any{tag}
		lb["pools"] = append(pools, pl)
		return made("pool", pl)
	case "pool-update":
		if e := needPool(); e != nil {
			return e
		}
		delete(spec, "protocol")
		apply(pool, spec, poolNames)
		setPersistence(pool, spec)
		return ok(map[string]any{"success": true, "steps": []any{}})
	case "pool-delete":
		if e := needPool(); e != nil {
			return e
		}
		_, i := find(pools, fmt.Sprint(o["poolId"]))
		lb["pools"] = append(pools[:i], pools[i+1:]...)
		return ok(map[string]any{"success": true, "steps": []any{}})
	case "monitor-create", "monitor-update":
		if e := needPool(); e != nil {
			return e
		}
		h, _ := pool["healthmonitor"].(map[string]any)
		if action == "monitor-create" {
			h = map[string]any{"id": p.id("hm"), "name": "", "max_retries_down": 3, "http_method": "", "url_path": "", "expected_codes": "", "admin_state_up": true,
				"tags": []any{tag}}
			if spec["type"] == "HTTP" {
				h["http_method"], h["url_path"], h["expected_codes"] = "GET", "/", "200"
			}
		}
		if action == "monitor-update" {
			delete(spec, "type") // can't change in the cloud
		}
		apply(h, spec, monitorNames)
		pool["healthmonitor"] = h
		if action == "monitor-create" {
			return made("health monitor", h) // a monitor's piece is the monitor (guide 9.6)
		}
		return ok(map[string]any{"success": true, "steps": []any{}})
	case "monitor-delete":
		if e := needPool(); e != nil {
			return e
		}
		if pool["healthmonitor"] == nil {
			return refuse("Pool %v has no health monitor.", o["poolId"])
		}
		pool["healthmonitor"] = nil
		return ok(map[string]any{"success": true, "steps": []any{}})
	case "member-add":
		if e := needPool(); e != nil {
			return e
		}
		if inst, has := spec["instanceId"]; has { // an instance: its two nodes on this network, as one group
			if fmt.Sprint(inst) != "301" {
				return refuse("Instance %v is not one this load balancer can use.", inst)
			}
			group := "morpheus:group:301" // mtncloud:allow-upstream-name (the plugin's tag on the wire)
			ms := []any{}
			for _, a := range []string{"10.0.0.31", "10.0.0.32"} {
				m := map[string]any{"id": p.id("m"), "name": "", "address": a, "protocol_port": spec["port"], "subnet_id": lb["vip_subnet_id"],
					"weight": spec["weight"], "backup": spec["backup"], "admin_state_up": true, "tags": []any{tag, group}}
				pool["members"] = append(list(pool, "members"), m)
				ms = append(ms, m)
			}
			out := ok(map[string]any{"success": true, "steps": []any{map[string]any{"kind": "members", "applied": true, "count": len(ms)}}})
			out["piece"] = map[string]any{"members": ms}
			return out
		}
		m := map[string]any{"id": p.id("m"), "name": "", "subnet_id": lb["vip_subnet_id"], "weight": 1, "backup": false, "admin_state_up": true, "monitor_address": nil, "monitor_port": nil,
			"tags": []any{tag}}
		apply(m, spec, memberNames)
		pool["members"] = append(list(pool, "members"), m)
		out := ok(map[string]any{"success": true, "steps": []any{map[string]any{"kind": "members", "name": "", "applied": true, "count": len(list(pool, "members"))}}})
		out["piece"] = m
		return out
	case "member-update", "member-remove":
		if e := needPool(); e != nil {
			return e
		}
		if inst, has := o["instanceId"]; has && action == "member-remove" {
			group, kept, n := "morpheus:group:"+fmt.Sprint(inst), []any{}, 0 // mtncloud:allow-upstream-name (the plugin's tag on the wire)
			for _, x := range list(pool, "members") {
				if slices.Contains(list(x.(map[string]any), "tags"), any(group)) {
					n++
					continue
				}
				kept = append(kept, x)
			}
			if n == 0 {
				return refuse("Pool %v has no members of instance %v.", o["poolId"], inst)
			}
			pool["members"] = kept
			return ok(map[string]any{"success": true, "steps": []any{}})
		}
		m, i := find(list(pool, "members"), fmt.Sprint(o["memberId"]))
		if m == nil {
			return refuse("Member %v is not in pool %v.", o["memberId"], o["poolId"])
		}
		if action == "member-remove" {
			pool["members"] = append(list(pool, "members")[:i], list(pool, "members")[i+1:]...)
		} else {
			apply(m, spec, memberNames)
		}
		return ok(map[string]any{"success": true, "steps": []any{}})
	case "l7policy-create", "l7policy-update", "l7policy-delete":
		l, _ := find(list(lb, "listeners"), fmt.Sprint(o["listenerId"]))
		if l == nil {
			return refuse("Listener %v is not on this load balancer.", o["listenerId"])
		}
		if l["protocol"] != "HTTP" && l["protocol"] != "TERMINATED_HTTPS" {
			return refuse("Listener %v is %v; L7 policies need an HTTP or TERMINATED_HTTPS listener.", o["listenerId"], l["protocol"])
		}
		pols := list(l, "l7policies_expanded")
		// As the plugin: a position another policy holds now is refused, on create and update.
		taken := func(self any) bool {
			for _, x := range pols {
				if q := x.(map[string]any); q["id"] != self && fmt.Sprint(q["position"]) == fmt.Sprint(spec["position"]) {
					return true
				}
			}
			return false
		}
		if action != "l7policy-delete" && taken(o["l7policyId"]) {
			return refuse("L7 policy '%v / %v': position %v is used by another policy on this listener", l["name"], spec["name"], spec["position"])
		}
		if action == "l7policy-create" {
			pol := map[string]any{"id": p.id("l7"), "tags": []any{tag}, "provisioning_status": "ACTIVE", "operating_status": "ONLINE"}
			if msg := setPolicy(pol, spec); msg != "" {
				return map[string]any{"success": false, "action": action, "error": msg,
					"result": map[string]any{"steps": []any{map[string]any{"kind": "L7 policy", "rolledBack": true}}}}
			}
			l["l7policies_expanded"] = placePolicy(pols, pol)
			return made("L7 policy", pol)
		}
		pol, i := find(pols, fmt.Sprint(o["l7policyId"]))
		if pol == nil {
			return refuse("L7 policy %v is not on listener %v.", o["l7policyId"], o["listenerId"])
		}
		if action == "l7policy-delete" {
			l["l7policies_expanded"] = placePolicy(append(pols[:i], pols[i+1:]...), nil)
			return ok(map[string]any{"success": true, "steps": []any{}})
		}
		if msg := setPolicy(pol, spec); msg != "" {
			return refuse("%s", msg)
		}
		l["l7policies_expanded"] = placePolicy(append(pols[:i:i], pols[i+1:]...), pol)
		out := ok(map[string]any{"success": true, "steps": []any{}})
		out["piece"] = pol
		return out
	case "fip-attach":
		if fip, _ := lb["fip"].(map[string]any); fip != nil {
			if o["floatingIpId"] == nil || o["floatingIpId"] == fip["id"] {
				return ok(map[string]any{"success": true, "existing": true, "floatingip": fip})
			}
			return refuse("Load balancer %s already has floating IP %v. Detach it first.", lbID, fip["floating_ip_address"])
		}
		fip := map[string]any{"id": "fip-held", "floating_ip_address": "196.0.0.9", "floating_network_id": "ext-02"}
		if o["floatingIpId"] == nil {
			if o["floatingIpPoolId"] != "ext-02" {
				return refuse("External network %v is not reachable from subnet sub-82.", o["floatingIpPoolId"])
			}
			fip = map[string]any{"id": p.id("fip"), "floating_ip_address": "196.0.0.10", "floating_network_id": "ext-02"}
		}
		lb["fip"] = fip
		return ok(map[string]any{"success": true, "floatingip": fip})
	case "fip-detach":
		if o["release"] != true && o["release"] != false {
			return refuse("release must be true or false.")
		}
		p.released = append(p.released, fmt.Sprint(o["release"]))
		delete(lb, "fip")
		return ok(map[string]any{"success": true})
	}
	return refuse("fake: unknown action %s", action)
}

// placePolicy puts pol (nil for none) at its asked position, or last when that is past the end,
// and numbers the listener's policies 1, 2, 3 again, as the cloud does.
func placePolicy(pols []any, pol map[string]any) []any {
	out := slices.Clone(pols)
	if pol != nil {
		at, _ := strconv.Atoi(fmt.Sprint(pol["position"]))
		at = min(max(at-1, 0), len(out))
		out = slices.Insert(out, at, any(pol))
	}
	for i, x := range out {
		x.(map[string]any)["position"] = i + 1
	}
	return out
}

// setPolicy applies an L7 policy spec as the plugin would, or says why it refuses it: the whole
// policy is sent, and rules replace every rule.
func setPolicy(pol, spec map[string]any) string {
	rules := []any{}
	for _, x := range list(spec, "rules") {
		r := x.(map[string]any)
		if r["value"] == "refuse-me" {
			return "L7 rule rejected: value refuse-me is not allowed"
		}
		rules = append(rules, map[string]any{"id": "r-" + fmt.Sprint(r["value"]), "type": r["type"], "compare_type": r["compareType"],
			"key": r["key"], "value": r["value"], "invert": r["invert"], "admin_state_up": true})
	}
	pol["name"], pol["description"], pol["action"], pol["position"] = spec["name"], spec["description"], spec["action"], spec["position"]
	pol["admin_state_up"], pol["rules_expanded"] = spec["adminStateUp"], rules
	pol["redirect_pool_id"], pol["redirect_url"], pol["redirect_prefix"], pol["redirect_http_code"] = spec["redirectPoolId"], spec["redirectUrl"], spec["redirectPrefix"], nil
	if a := spec["action"]; a == "REDIRECT_TO_URL" || a == "REDIRECT_PREFIX" {
		pol["redirect_http_code"] = 302 // the cloud's default
		if c, has := spec["redirectHttpCode"]; has {
			pol["redirect_http_code"] = c
		}
	}
	return ""
}

var requestIDForm = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// taggedPiece is the piece a create with this request tag made, if any.
func taggedPiece(lb map[string]any, tag string) map[string]any {
	has := func(x map[string]any) bool {
		for _, t := range list(x, "tags") {
			if t == tag {
				return true
			}
		}
		return false
	}
	for _, l := range list(lb, "listeners") {
		if has(l.(map[string]any)) {
			return l.(map[string]any)
		}
		for _, pol := range list(l.(map[string]any), "l7policies_expanded") {
			if has(pol.(map[string]any)) {
				return pol.(map[string]any)
			}
		}
	}
	for _, x := range list(lb, "pools") {
		pl := x.(map[string]any)
		if has(pl) {
			return pl
		}
		if h, _ := pl["healthmonitor"].(map[string]any); h != nil && has(h) {
			return h
		}
		for _, m := range list(pl, "members") {
			if has(m.(map[string]any)) {
				return m.(map[string]any)
			}
		}
	}
	return nil
}

func setPersistence(pool, spec map[string]any) {
	if v, has := spec["sessionPersistence"]; has {
		if v == nil {
			pool["session_persistence"] = nil
		} else {
			pool["session_persistence"] = map[string]any{"type": v, "cookie_name": spec["cookieName"]}
		}
	}
}

// deleteListener removes a listener outside Terraform.
func (p *lbPlugin) deleteListener() {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, lb := range p.lbs {
		lb["listeners"] = []any{}
	}
}

func TestLoadBalancerResources(t *testing.T) {
	f := newFakeAPI(t)
	plugin := newLBPlugin()
	f.lb = plugin
	cfg := func(limit, cidrs, algorithm string, weight, port, delay int) string {
		return f.providerConfig() + fmt.Sprintf(`
resource "mtncloud_load_balancer" "web" {
  network_id = 114
  name       = "web"
  tags       = ["team-web"]
}
resource "mtncloud_load_balancer_pool" "web" {
  network_id          = 114
  load_balancer_id    = mtncloud_load_balancer.web.id
  name                = "web-pool"
  protocol            = "HTTP"
  algorithm           = %q
  session_persistence = "APP_COOKIE"
  cookie_name         = "SESSION"
}
resource "mtncloud_load_balancer_health_monitor" "web" {
  network_id       = 114
  load_balancer_id = mtncloud_load_balancer.web.id
  pool_id          = mtncloud_load_balancer_pool.web.id
  type             = "HTTP"
  delay            = %d
  timeout          = 3
  max_retries      = 3
}
resource "mtncloud_load_balancer_member" "a" {
  network_id       = 114
  load_balancer_id = mtncloud_load_balancer.web.id
  pool_id          = mtncloud_load_balancer_pool.web.id
  address          = "10.0.0.5"
  port             = %d
  weight           = %d
}
resource "mtncloud_load_balancer_listener" "http" {
  network_id       = 114
  load_balancer_id = mtncloud_load_balancer.web.id
  protocol         = "HTTP"
  port             = 80
  default_pool_id  = mtncloud_load_balancer_pool.web.id
  %s
  %s
}
`, algorithm, delay, port, weight, limit, cidrs)
	}
	first := cfg("", `allowed_cidrs = ["203.0.113.0/24"]`, "ROUND_ROBIN", 1, 8080, 5)
	changed := cfg("connection_limit = 500", "", "LEAST_CONNECTIONS", 3, 8080, 10)
	empty := resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()}}
	update := func(addr string) plancheck.PlanCheck {
		return plancheck.ExpectResourceAction(addr, plancheck.ResourceActionUpdate)
	}
	importOf := func(addr string, id func(*terraform.State) (string, error)) resource.TestStep {
		return resource.TestStep{ResourceName: addr, ImportState: true, ImportStateVerify: true, ImportStateIdFunc: id,
			ImportStateVerifyIgnore: []string{"timeouts"}, Config: changed}
	}
	attr := func(s *terraform.State, addr, k string) string {
		return s.RootModule().Resources[addr].Primary.Attributes[k]
	}
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories(),
		CheckDestroy: func(*terraform.State) error {
			plugin.mu.Lock()
			defer plugin.mu.Unlock()
			if len(plugin.lbs) != 0 {
				return fmt.Errorf("%d load balancers left", len(plugin.lbs))
			}
			return nil
		},
		Steps: []resource.TestStep{
			{
				Config: first,
				ConfigStateChecks: []statecheck.StateCheck{
					// The VIP defaults to the network's own subnet, by its cloud ID.
					statecheck.ExpectKnownValue("mtncloud_load_balancer.web", tfjsonpath.New("vip_subnet_id"), knownvalue.StringExact("sub-82")),
					statecheck.ExpectKnownValue("mtncloud_load_balancer.web", tfjsonpath.New("provisioning_status"), knownvalue.StringExact("ACTIVE")),
					// The plugin's own request tag isn't the user's.
					statecheck.ExpectKnownValue("mtncloud_load_balancer.web", tfjsonpath.New("tags"), knownvalue.SetExact([]knownvalue.Check{knownvalue.StringExact("team-web")})),
					statecheck.ExpectKnownValue("mtncloud_load_balancer_listener.http", tfjsonpath.New("connection_limit"), knownvalue.Int64Exact(-1)),
					statecheck.ExpectKnownValue("mtncloud_load_balancer_health_monitor.web", tfjsonpath.New("url_path"), knownvalue.StringExact("/")),
					statecheck.ExpectKnownValue("mtncloud_load_balancer_member.a", tfjsonpath.New("subnet_id"), knownvalue.StringExact("sub-82")),
				},
			},
			{Config: first, ConfigPlanChecks: empty},
			{
				// In place: no piece is replaced, and taking allowed_cidrs out removes the restriction.
				Config: changed,
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					update("mtncloud_load_balancer_listener.http"), update("mtncloud_load_balancer_pool.web"),
					update("mtncloud_load_balancer_member.a"), update("mtncloud_load_balancer_health_monitor.web"),
					plancheck.ExpectResourceAction("mtncloud_load_balancer.web", plancheck.ResourceActionNoop),
				}},
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue("mtncloud_load_balancer_listener.http", tfjsonpath.New("allowed_cidrs"), knownvalue.Null()),
					statecheck.ExpectKnownValue("mtncloud_load_balancer_listener.http", tfjsonpath.New("connection_limit"), knownvalue.Int64Exact(500)),
				},
			},
			{Config: changed, ConfigPlanChecks: empty},
			importOf("mtncloud_load_balancer.web", func(s *terraform.State) (string, error) {
				return "114/" + attr(s, "mtncloud_load_balancer.web", "id"), nil
			}),
			importOf("mtncloud_load_balancer_listener.http", func(s *terraform.State) (string, error) {
				return "114/" + attr(s, "mtncloud_load_balancer.web", "id") + "/" + attr(s, "mtncloud_load_balancer_listener.http", "id"), nil
			}),
			importOf("mtncloud_load_balancer_pool.web", func(s *terraform.State) (string, error) {
				return "114/" + attr(s, "mtncloud_load_balancer.web", "id") + "/" + attr(s, "mtncloud_load_balancer_pool.web", "id"), nil
			}),
			importOf("mtncloud_load_balancer_health_monitor.web", func(s *terraform.State) (string, error) {
				return "114/" + attr(s, "mtncloud_load_balancer.web", "id") + "/" + attr(s, "mtncloud_load_balancer_pool.web", "id"), nil
			}),
			importOf("mtncloud_load_balancer_member.a", func(s *terraform.State) (string, error) {
				return "114/" + attr(s, "mtncloud_load_balancer.web", "id") + "/" + attr(s, "mtncloud_load_balancer_pool.web", "id") + "/" +
					attr(s, "mtncloud_load_balancer_member.a", "id"), nil
			}),
			{
				// The member's port can't change in the cloud: it is replaced.
				Config: cfg("connection_limit = 500", "", "LEAST_CONNECTIONS", 3, 8081, 10),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction("mtncloud_load_balancer_member.a", plancheck.ResourceActionDestroyBeforeCreate),
				}},
			},
			{
				// Deleted outside Terraform: the next plan creates it again.
				PreConfig:          plugin.deleteListener,
				Config:             cfg("connection_limit = 500", "", "LEAST_CONNECTIONS", 3, 8081, 10),
				PlanOnly:           true,
				ExpectNonEmptyPlan: true,
			},
			{Config: cfg("connection_limit = 500", "", "LEAST_CONNECTIONS", 3, 8081, 10)},
			{Config: cfg("connection_limit = 500", "", "LEAST_CONNECTIONS", 3, 8081, 10), ConfigPlanChecks: empty},
		},
	})
}

// The load balancer's own fields change in place with one loadbalancer-update; nothing is replaced.
func TestLoadBalancerResource_UpdatesInPlace(t *testing.T) {
	f := newFakeAPI(t)
	plugin := newLBPlugin()
	f.lb = plugin
	cfg := func(body string) string {
		return f.providerConfig() + `
resource "mtncloud_load_balancer" "web" {
  network_id = 114
` + body + `
}
`
	}
	first := cfg(`  name        = "web"
  description = "front door"
  tags        = ["team-web"]`)
	changed := cfg(`  name    = "web-2"
  enabled = false`)
	lb := "mtncloud_load_balancer.web"
	empty := resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()}}
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories(),
		Steps: []resource.TestStep{
			{Config: first, ConfigStateChecks: []statecheck.StateCheck{
				statecheck.ExpectKnownValue(lb, tfjsonpath.New("enabled"), knownvalue.Bool(true)),
			}},
			{Config: first, ConfigPlanChecks: empty},
			{
				// Renamed, description and tags taken out, and switched off: all in place.
				Config: changed,
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction(lb, plancheck.ResourceActionUpdate),
				}},
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(lb, tfjsonpath.New("name"), knownvalue.StringExact("web-2")),
					statecheck.ExpectKnownValue(lb, tfjsonpath.New("description"), knownvalue.StringExact("")),
					statecheck.ExpectKnownValue(lb, tfjsonpath.New("tags"), knownvalue.Null()),
					statecheck.ExpectKnownValue(lb, tfjsonpath.New("enabled"), knownvalue.Bool(false)),
				},
				Check: func(*terraform.State) error {
					plugin.mu.Lock()
					defer plugin.mu.Unlock()
					if plugin.runs["create"] != 1 || plugin.runs["loadbalancer-update"] != 1 {
						return fmt.Errorf("creates %d, field updates %d; want 1 and 1", plugin.runs["create"], plugin.runs["loadbalancer-update"])
					}
					for _, l := range plugin.lbs {
						if tags := list(l, "tags"); len(tags) != 1 || !strings.HasPrefix(fmt.Sprint(tags[0]), "mtncloud-req-") {
							return fmt.Errorf("tags %v, want only the request tag left", tags)
						}
					}
					return nil
				},
			},
			{Config: changed, ConfigPlanChecks: empty},
		},
	})
}

// A load balancer can't be created switched off, so one with enabled = false is switched off
// straight after its create.
func TestLoadBalancerResource_CreatedDisabled(t *testing.T) {
	f := newFakeAPI(t)
	plugin := newLBPlugin()
	f.lb = plugin
	cfg := f.providerConfig() + `
resource "mtncloud_load_balancer" "web" {
  network_id = 114
  name       = "web"
  enabled    = false
}
`
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories(),
		Steps: []resource.TestStep{
			{Config: cfg, ConfigStateChecks: []statecheck.StateCheck{
				statecheck.ExpectKnownValue("mtncloud_load_balancer.web", tfjsonpath.New("enabled"), knownvalue.Bool(false)),
			}},
			{Config: cfg, ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()}}},
		},
	})
}

// Taking a listener's description and SNI certificates, or a member's health-check address and
// port, out of the configuration clears them in place (guide 9.6): nothing is replaced.
func TestLoadBalancerResources_ClearInPlace(t *testing.T) {
	f := newFakeAPI(t)
	plugin := newLBPlugin()
	f.lb = plugin
	cfg := func(tls, mon string) string {
		return f.providerConfig() + `
resource "mtncloud_load_balancer" "web" {
  network_id = 114
  name       = "web"
}
resource "mtncloud_load_balancer_pool" "web" {
  network_id       = 114
  load_balancer_id = mtncloud_load_balancer.web.id
  protocol         = "HTTP"
  algorithm        = "ROUND_ROBIN"
}
resource "mtncloud_load_balancer_member" "a" {
  network_id       = 114
  load_balancer_id = mtncloud_load_balancer.web.id
  pool_id          = mtncloud_load_balancer_pool.web.id
  address          = "10.0.0.5"
  port             = 8080
  ` + mon + `
}
resource "mtncloud_load_balancer_listener" "https" {
  network_id       = 114
  load_balancer_id = mtncloud_load_balancer.web.id
  protocol         = "TERMINATED_HTTPS"
  port             = 443
  ` + tls + `
}
`
	}
	tls := `tls_certificate_ref = "ref-1"`
	set := cfg(tls+"\n  description = \"secure\"\n  sni_certificate_refs = [\"ref-2\"]", "monitor_address = \"10.0.0.6\"\n  monitor_port = 9000")
	listener, member := "mtncloud_load_balancer_listener.https", "mtncloud_load_balancer_member.a"
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories(),
		Steps: []resource.TestStep{
			{Config: set, ConfigStateChecks: []statecheck.StateCheck{
				statecheck.ExpectKnownValue(listener, tfjsonpath.New("description"), knownvalue.StringExact("secure")),
			}},
			{Config: set, ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()}}},
			{
				Config: cfg(tls, ""),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction(listener, plancheck.ResourceActionUpdate),
					plancheck.ExpectResourceAction(member, plancheck.ResourceActionUpdate),
				}},
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(listener, tfjsonpath.New("description"), knownvalue.StringExact("")),
					statecheck.ExpectKnownValue(listener, tfjsonpath.New("sni_certificate_refs"), knownvalue.Null()),
					statecheck.ExpectKnownValue(member, tfjsonpath.New("monitor_address"), knownvalue.Null()),
					statecheck.ExpectKnownValue(member, tfjsonpath.New("monitor_port"), knownvalue.Null()),
				},
				Check: func(*terraform.State) error {
					plugin.mu.Lock()
					defer plugin.mu.Unlock()
					for _, lb := range plugin.lbs {
						for _, l := range list(lb, "listeners") {
							if refs := list(l.(map[string]any), "sni_container_refs"); len(refs) != 0 {
								return fmt.Errorf("SNI refs %v left in the cloud", refs)
							}
						}
						m := list(list(lb, "pools")[0].(map[string]any), "members")[0].(map[string]any)
						if m["monitor_address"] != nil || m["monitor_port"] != nil {
							return fmt.Errorf("monitor override %v:%v left in the cloud", m["monitor_address"], m["monitor_port"])
						}
						// "" on the wire, not a missing key: the create default is "" too.
						if d, has := plugin.sent["listener-update"]["description"]; !has || d != "" {
							return fmt.Errorf("listener-update sent description %v (present %v), want \"\"", d, has)
						}
						if plugin.runs["listener-create"] != 1 || plugin.runs["member-add"] != 1 {
							return fmt.Errorf("%d listener creates, %d member adds: something was replaced", plugin.runs["listener-create"], plugin.runs["member-add"])
						}
					}
					return nil
				},
			},
			{
				// A TERMINATED_HTTPS listener must keep a certificate: refused at plan time.
				Config: cfg("", ""), PlanOnly: true, ExpectError: regexp.MustCompile(`(?s)Missing certificate`),
			},
			// Last, so the closing destroy runs on a valid configuration.
			{Config: cfg(tls, ""), ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()}}},
		},
	})
}

func TestLoadBalancerResources_PlanTimeChecks(t *testing.T) {
	f := newFakeAPI(t)
	f.lb = newLBPlugin()
	for name, c := range map[string]struct{ body, want string }{
		"protocol": {`resource "mtncloud_load_balancer_listener" "x" {
  network_id = 114
  load_balancer_id = "lb-1"
  protocol = "FTP"
  port = 21
}`, `protocol value must be one of`},
		"port": {`resource "mtncloud_load_balancer_listener" "x" {
  network_id = 114
  load_balancer_id = "lb-1"
  protocol = "TCP"
  port = 70000
}`, `port value must be between 1 and 65535`},
		"cookie": {`resource "mtncloud_load_balancer_pool" "x" {
  network_id = 114
  load_balancer_id = "lb-1"
  protocol = "HTTP"
  algorithm = "ROUND_ROBIN"
  session_persistence = "APP_COOKIE"
}`, `needs cookie_name`},
		"weight": {`resource "mtncloud_load_balancer_member" "x" {
  network_id = 114
  load_balancer_id = "lb-1"
  pool_id = "p-1"
  address = "10.0.0.5"
  port = 80
  weight = 300
}`, `weight value must be between 0 and 256`},
		"monitor timeout": {`resource "mtncloud_load_balancer_health_monitor" "x" {
  network_id = 114
  load_balancer_id = "lb-1"
  pool_id = "p-1"
  type = "HTTP"
  delay = 5
  timeout = 5
  max_retries = 3
}`, `must be less than delay`},
		"http method": {`resource "mtncloud_load_balancer_health_monitor" "x" {
  network_id = 114
  load_balancer_id = "lb-1"
  pool_id = "p-1"
  type = "HTTP"
  delay = 5
  timeout = 3
  max_retries = 3
  http_method = "get"
}`, `http_method value must be one of`},
	} {
		t.Run(name, func(t *testing.T) {
			resource.UnitTest(t, resource.TestCase{
				ProtoV6ProviderFactories: factories(),
				Steps:                    []resource.TestStep{{Config: f.providerConfig() + c.body, PlanOnly: true, ExpectError: regexp.MustCompile(`(?s)` + c.want)}},
			})
		})
	}
}
