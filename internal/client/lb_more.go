package client

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"sync"
	"time"
)

// L7 policies, floating IPs and instance members: the rest of the plugin's piece actions (guide
// 9.6 and 9.6.2).

// LBL7Policy is one L7 policy of a listener, with its rules. Policies are tried in position order;
// the first whose rules all match acts.
type LBL7Policy struct {
	ID                 string     `json:"id"`
	Name               string     `json:"name"`
	Description        string     `json:"description"`
	Action             string     `json:"action"`
	Position           int64      `json:"position"`
	RedirectPoolID     string     `json:"redirect_pool_id"`
	RedirectURL        string     `json:"redirect_url"`
	RedirectPrefix     string     `json:"redirect_prefix"`
	RedirectHTTPCode   int64      `json:"redirect_http_code"`
	AdminStateUp       bool       `json:"admin_state_up"`
	ProvisioningStatus string     `json:"provisioning_status"`
	OperatingStatus    string     `json:"operating_status"`
	Tags               []string   `json:"tags"`
	Rules              []LBL7Rule `json:"rules_expanded"`
	// RulesError is set when the rules could not be read: then Rules is not "none".
	RulesError string `json:"rules_error"`
}

// LBL7Rule is one condition of an L7 policy. A policy's rules must all match.
type LBL7Rule struct {
	ID           string `json:"id"`
	Type         string `json:"type"`
	CompareType  string `json:"compare_type"`
	Key          string `json:"key"`
	Value        string `json:"value"`
	Invert       bool   `json:"invert"`
	AdminStateUp bool   `json:"admin_state_up"`
}

// LBL7PolicySpec is an L7 policy to create, or its new settings: an update sends the whole policy,
// and Rules replaces every rule.
type LBL7PolicySpec struct {
	Name, Description string
	Action            string
	Position          int64
	RedirectPoolID    string
	RedirectURL       string
	RedirectPrefix    string
	RedirectHTTPCode  int64 // 0: the cloud's default
	Enabled           bool
	Rules             []LBL7RuleSpec
}

// LBL7RuleSpec is one rule of a policy spec.
type LBL7RuleSpec struct {
	Type, CompareType, Key, Value string
	Invert                        bool
}

func (s LBL7PolicySpec) body() map[string]any {
	rules := make([]map[string]any, 0, len(s.Rules))
	for _, r := range s.Rules {
		m := map[string]any{"type": r.Type, "compareType": r.CompareType, "value": r.Value, "invert": r.Invert, "adminStateUp": true}
		if r.Key != "" {
			m["key"] = r.Key
		}
		rules = append(rules, m)
	}
	b := map[string]any{"name": s.Name, "description": s.Description, "action": s.Action, "position": s.Position,
		"adminStateUp": s.Enabled, "rules": rules}
	switch s.Action {
	case "REDIRECT_TO_POOL":
		b["redirectPoolId"] = s.RedirectPoolID
	case "REDIRECT_TO_URL":
		b["redirectUrl"] = s.RedirectURL
	case "REDIRECT_PREFIX":
		b["redirectPrefix"] = s.RedirectPrefix
	}
	if s.RedirectHTTPCode != 0 {
		b["redirectHttpCode"] = s.RedirectHTTPCode
	}
	return b
}

// ListenerOf is the listener with this ID on the load balancer, or nil.
func ListenerOf(lb *LoadBalancer, id string) *LBListener {
	for i := range lb.Listeners {
		if lb.Listeners[i].ID == id {
			return &lb.Listeners[i]
		}
	}
	return nil
}

// CreateLBL7Policy creates an L7 policy with its rules on a listener. The plugin creates the policy
// first, then each rule, and removes the policy again when a rule is refused, so a policy is
// never left with only some of its rules.
func (c *Client) CreateLBL7Policy(ctx context.Context, networkID int64, lbID, listenerID string, s LBL7PolicySpec) (string, error) {
	done, err := c.l7Turn(ctx, listenerID, s.Position)
	if err != nil {
		return "", err
	}
	defer done()
	return c.createLBL7Policy(ctx, networkID, lbID, listenerID, s)
}

// l7Gather is how long a policy create waits for the other creates of the same apply to start.
const l7Gather = 1500 * time.Millisecond

// l7Gate holds the policy creates under way on one listener, by their planned position.
type l7Gate struct {
	mu      sync.Mutex
	pending map[*int64]bool
}

// l7Turn waits until no create or move to a lower position on the same listener is under way. The
// plugin inserts a policy at its position and shifts the ones at and below it down (D-033), so
// placing one apply's policies in ascending position order lands each where it is configured.
// Terraform starts them together, in any order; each waits l7Gather for the others to start, then
// for the lower ones to finish. done ends this policy's turn.
func (c *Client) l7Turn(ctx context.Context, listenerID string, pos int64) (done func(), err error) {
	v, _ := c.l7Gates.LoadOrStore(listenerID, &l7Gate{pending: map[*int64]bool{}})
	g := v.(*l7Gate)
	me := &pos
	g.mu.Lock()
	g.pending[me] = true
	g.mu.Unlock()
	done = func() {
		g.mu.Lock()
		delete(g.pending, me)
		g.mu.Unlock()
	}
	lower := func() bool {
		g.mu.Lock()
		defer g.mu.Unlock()
		for p := range g.pending {
			if p != me && *p < pos {
				return true
			}
		}
		return false
	}
	wait := time.NewTimer(l7Gather)
	defer wait.Stop()
	for first := true; first || lower(); first = false {
		select {
		case <-wait.C:
			wait.Reset(200 * time.Millisecond)
		case <-ctx.Done():
			done()
			return nil, fmt.Errorf("waiting for the L7 policies before position %d on this listener: %w", pos, ctx.Err())
		}
	}
	return done, nil
}

func (c *Client) createLBL7Policy(ctx context.Context, networkID int64, lbID, listenerID string, s LBL7PolicySpec) (string, error) {
	return c.pieceCreate(ctx, networkID, lbID, "l7policy-create", "L7 policy", map[string]any{"listenerId": listenerID, "spec": s.body()}, func(lb *LoadBalancer, tag string) string {
		if l := ListenerOf(lb, listenerID); l != nil {
			for _, p := range l.L7Policies { // a policy has nothing unique but its request tag
				if hasTag(p.Tags, tag) {
					return p.ID
				}
			}
		}
		return ""
	})
}

// UpdateLBL7Policy changes an L7 policy. moved says its position changes: it then takes its turn
// with the apply's other creates and moves on the listener (l7Turn).
func (c *Client) UpdateLBL7Policy(ctx context.Context, networkID int64, lbID, listenerID, id string, s LBL7PolicySpec, moved bool) error {
	if moved {
		done, err := c.l7Turn(ctx, listenerID, s.Position)
		if err != nil {
			return err
		}
		defer done()
	}
	_, err := c.lbWrite(ctx, networkID, lbID, "l7policy-update", map[string]any{"listenerId": listenerID, "l7policyId": id, "spec": s.body()})
	return err
}

func (c *Client) DeleteLBL7Policy(ctx context.Context, networkID int64, lbID, listenerID, id string) error {
	return c.lbRemove(ctx, networkID, lbID, "l7policy-delete", map[string]any{"listenerId": listenerID, "l7policyId": id})
}

// LBInstanceMemberSpec adds every address an instance has on the load balancer's network, as one
// group the console keeps in step as the instance scales.
type LBInstanceMemberSpec struct {
	InstanceID int64
	Port       int64
	Weight     int64
	Backup     bool
}

// instanceGroupTag marks the members of one instance's group; it is the plugin's own tag.
func instanceGroupTag(instanceID int64) string {
	return "morpheus:group:" + strconv.FormatInt(instanceID, 10) // mtncloud:allow-upstream-name (the plugin's tag on the wire)
}

// InstanceMembers are the members of a pool that belong to an instance's group.
func InstanceMembers(p *LBPool, instanceID int64) []LBMember {
	tag := instanceGroupTag(instanceID)
	var out []LBMember
	for _, m := range p.Members {
		if hasTag(m.Tags, tag) {
			out = append(out, m)
		}
	}
	return out
}

// AddLBInstanceMembers adds an instance's addresses to a pool. The instance must be one the
// console's member picker offers for this load balancer: the tenant's own, with an address on its
// network. The members are read back from the load balancer.
func (c *Client) AddLBInstanceMembers(ctx context.Context, networkID int64, lbID, poolID string, s LBInstanceMemberSpec) error {
	spec := map[string]any{"instanceId": strconv.FormatInt(s.InstanceID, 10), "port": s.Port, "weight": s.Weight, "backup": s.Backup}
	in := map[string]any{"poolId": poolID, "spec": spec, "requestId": newRequestID()}
	_, err := c.lbWrite(ctx, networkID, lbID, "member-add", in)
	return err
}

// RemoveLBInstanceMembers removes every member of an instance's group from a pool. None left is
// not an error.
func (c *Client) RemoveLBInstanceMembers(ctx context.Context, networkID int64, lbID, poolID string, instanceID int64) error {
	return c.lbRemove(ctx, networkID, lbID, "member-remove", map[string]any{"poolId": poolID, "instanceId": strconv.FormatInt(instanceID, 10)})
}

// LBFloatingIP is a public address attached to a load balancer's VIP.
type LBFloatingIP struct {
	ID      string `json:"id"`
	Address string `json:"floating_ip_address"`
	PoolID  string `json:"floating_network_id"`
}

// AttachLBFloatingIP attaches a held address (addressID) or a new one from a pool (poolID, its
// external ID) to the load balancer. Attaching again what is already attached changes nothing and
// answers the same address, so a retry is safe.
func (c *Client) AttachLBFloatingIP(ctx context.Context, networkID int64, lbID, addressID, poolID string) (*LBFloatingIP, error) {
	unlock, err := c.lbLock(ctx, lbID)
	if err != nil {
		return nil, err
	}
	defer unlock()
	in := map[string]any{"networkId": itoa(networkID), "lbId": lbID}
	if addressID != "" {
		in["floatingIpId"] = addressID
	} else {
		in["floatingIpPoolId"] = poolID
	}
	raw, err := c.runLB(ctx, "fip-attach", in)
	c.lbChanged(networkID)
	if err != nil {
		return nil, err
	}
	var out struct {
		FloatingIP *LBFloatingIP `json:"floatingip"`
	}
	if json.Unmarshal(raw, &out) != nil || out.FloatingIP == nil || out.FloatingIP.ID == "" {
		return nil, fmt.Errorf("load balancer floating IP attach: the answer names no floating IP")
	}
	return out.FloatingIP, nil
}

// DetachLBFloatingIP takes the public address off the load balancer; release gives it back to its
// pool, otherwise the project keeps it. Nothing attached is not an error.
func (c *Client) DetachLBFloatingIP(ctx context.Context, networkID int64, lbID string, release bool) error {
	unlock, err := c.lbLock(ctx, lbID)
	if err != nil {
		return err
	}
	defer unlock()
	_, err = c.runLB(ctx, "fip-detach", map[string]any{"networkId": itoa(networkID), "lbId": lbID, "release": release})
	c.lbChanged(networkID)
	if IsLBMissing(err) {
		return nil
	}
	return err
}

// LoadBalancerRow is the load balancer's row in its network's list, read once per run: the list,
// not the read, carries the floating IP. nil when the load balancer isn't listed.
func (c *Client) LoadBalancerRow(ctx context.Context, networkID int64, id string) (*LoadBalancerSummary, error) {
	l, err := cached(ctx, &c.cache, fmt.Sprintf("lb-read:%d:list", networkID), func(ctx context.Context) (*LoadBalancerList, error) {
		return c.ListLoadBalancers(ctx, networkID, LBListFilter{})
	})
	if err != nil {
		return nil, err
	}
	for i := range l.LoadBalancers {
		if l.LoadBalancers[i].ID == id {
			return &l.LoadBalancers[i], nil
		}
	}
	return nil, nil
}
