package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// Load balancer writes, through the same task as the reads (D-031, the plugin's
// TERRAFORM-AGENT-GUIDE.md section 9). Facts from the live probe of 2026-10-29
// (dev-docs/api-research.md) that differ from the guide:
//   - create answers with the load balancer still PENDING_CREATE, and delete with
//     {deleting: true}: the client waits for ACTIVE, or for gone, itself;
//   - a piece create answers steps[] ({kind, name, applied, created, id}), not piece: the new
//     piece's ID is the step of its kind.
//
// Every piece write re-reads the load balancer in the plugin and saves it, and the plugin refuses a
// second save while one runs, so writes to one load balancer take turns here (lbLock). Each write
// drops the cached reads of its network (B-021).

// LBListenerSpec is a listener to create, or the fields to change. A nil field is left as it is;
// DefaultPoolID or TLSContainerRef set to "" clears it.
type LBListenerSpec struct {
	Name, Description      *string
	Protocol               string // create only
	Port                   int64  // create only
	DefaultPoolID          *string
	ConnectionLimit        *int64
	AllowedCIDRs           []string // nil: leave; empty: none
	Enabled                *bool
	TLSContainerRef        *string
	SNIContainerRefs       []string
	ClientDataTimeout      *int64
	MemberConnectTimeout   *int64
	MemberDataTimeout      *int64
	TCPInspectTimeout      *int64
	allowedCIDRsMeansClear bool
}

// ClearAllowedCIDRs makes an update send an empty list, which allows every source again.
func (s *LBListenerSpec) ClearAllowedCIDRs() { s.allowedCIDRsMeansClear = true }

func (s LBListenerSpec) body() map[string]any {
	b := map[string]any{}
	if s.Protocol != "" {
		b["protocol"], b["port"] = s.Protocol, s.Port
	}
	putStr(b, "name", s.Name)
	putStr(b, "description", s.Description)
	putClearable(b, "defaultPoolId", s.DefaultPoolID)
	putInt(b, "connectionLimit", s.ConnectionLimit)
	if s.AllowedCIDRs != nil || s.allowedCIDRsMeansClear {
		b["allowedCidrs"] = nonNil(s.AllowedCIDRs)
	}
	if s.Enabled != nil {
		b["adminStateUp"] = *s.Enabled
	}
	putClearable(b, "defaultTlsContainerRef", s.TLSContainerRef)
	if s.SNIContainerRefs != nil {
		b["sniContainerRefs"] = s.SNIContainerRefs
	}
	putInt(b, "timeoutClientData", s.ClientDataTimeout)
	putInt(b, "timeoutMemberConnect", s.MemberConnectTimeout)
	putInt(b, "timeoutMemberData", s.MemberDataTimeout)
	putInt(b, "timeoutTcpInspect", s.TCPInspectTimeout)
	return b
}

// LBPoolSpec is a pool to create, or the fields to change. SessionPersistence "" removes it.
type LBPoolSpec struct {
	Name, Description  *string
	Protocol           string // create only
	Algorithm          string
	SessionPersistence *string
	CookieName         *string
	TLSEnabled         *bool
	Enabled            *bool
}

func (s LBPoolSpec) body() map[string]any {
	b := map[string]any{}
	if s.Protocol != "" {
		b["protocol"] = s.Protocol
	}
	if s.Algorithm != "" {
		b["algorithm"] = s.Algorithm
	}
	putStr(b, "name", s.Name)
	putStr(b, "description", s.Description)
	putClearable(b, "sessionPersistence", s.SessionPersistence)
	putStr(b, "cookieName", s.CookieName)
	if s.TLSEnabled != nil {
		b["tlsEnabled"] = *s.TLSEnabled
	}
	if s.Enabled != nil {
		b["adminStateUp"] = *s.Enabled
	}
	return b
}

// LBMonitorSpec is a pool's health monitor, or the fields to change.
type LBMonitorSpec struct {
	Name                               *string
	Type                               string // create only
	Delay, Timeout, MaxRetries         *int64
	MaxRetriesDown                     *int64
	HTTPMethod, URLPath, ExpectedCodes *string
	Enabled                            *bool
}

func (s LBMonitorSpec) body() map[string]any {
	b := map[string]any{}
	if s.Type != "" {
		b["type"] = s.Type
	}
	putStr(b, "name", s.Name)
	putInt(b, "delay", s.Delay)
	putInt(b, "timeout", s.Timeout)
	putInt(b, "maxRetries", s.MaxRetries)
	putInt(b, "maxRetriesDown", s.MaxRetriesDown)
	putStr(b, "httpMethod", s.HTTPMethod)
	putStr(b, "urlPath", s.URLPath)
	putStr(b, "expectedCodes", s.ExpectedCodes)
	if s.Enabled != nil {
		b["adminStateUp"] = *s.Enabled
	}
	return b
}

// LBMemberSpec is an external member (a server outside the console, by address), or the fields
// to change. Address and Port can't change.
type LBMemberSpec struct {
	Name           *string
	Address        string // create only
	Port           int64  // create only
	SubnetID       string // create only
	Weight         *int64
	Backup         *bool
	MonitorAddress *string
	MonitorPort    *int64
	Enabled        *bool
}

func (s LBMemberSpec) body() map[string]any {
	b := map[string]any{}
	if s.Address != "" {
		b["address"], b["port"] = s.Address, s.Port
	}
	if s.SubnetID != "" {
		b["subnetId"] = s.SubnetID
	}
	putStr(b, "name", s.Name)
	putInt(b, "weight", s.Weight)
	if s.Backup != nil {
		b["backup"] = *s.Backup
	}
	putStr(b, "monitorAddress", s.MonitorAddress)
	putInt(b, "monitorPort", s.MonitorPort)
	if s.Enabled != nil {
		b["adminStateUp"] = *s.Enabled
	}
	return b
}

// LoadBalancerSpec is a new load balancer, created empty: its listeners and pools are their own
// resources.
type LoadBalancerSpec struct {
	Name, Description string
	VIPSubnetID       string
	VIPAddress        string // "" lets the cloud choose
	Tags              []string
}

// LoadBalancerUpdate is a change to the load balancer's own fields; nil leaves a field alone.
// Tags replace the user's tags ([] removes them); the plugin's request tag always stays.
type LoadBalancerUpdate struct {
	Name, Description *string
	Tags              *[]string
	Enabled           *bool
}

func (s LoadBalancerUpdate) body() map[string]any {
	b := map[string]any{}
	putStr(b, "name", s.Name)
	putStr(b, "description", s.Description) // "" clears it
	if s.Tags != nil {
		b["tags"] = nonNil(*s.Tags)
	}
	if s.Enabled != nil {
		b["adminStateUp"] = *s.Enabled
	}
	return b
}

func putStr(b map[string]any, k string, v *string) {
	if v != nil {
		b[k] = *v
	}
}

func putInt(b map[string]any, k string, v *int64) {
	if v != nil {
		b[k] = *v
	}
}

// putClearable sends v, or an explicit null when v is "", which the plugin reads as "remove it".
func putClearable(b map[string]any, k string, v *string) {
	switch {
	case v == nil:
	case *v == "":
		b[k] = nil
	default:
		b[k] = *v
	}
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// lbLock makes writes to one load balancer take turns.
func (c *Client) lbLock(ctx context.Context, lbID string) (func(), error) {
	return c.keyLock(ctx, "lb:"+lbID, "load balancer "+lbID)
}

// lbChanged drops the cached reads of a network's load balancers after a write (B-021).
func (c *Client) lbChanged(networkID int64) {
	c.cache.forget(fmt.Sprintf("lb-read:%d:", networkID))
}

// lbStep is one entry of a piece write's answer.
type lbStep struct {
	Kind    string `json:"kind"`
	Name    string `json:"name"`
	Applied bool   `json:"applied"`
	Created bool   `json:"created"`
	ID      string `json:"id"`
	Error   string `json:"error"`
}

// writeAnswer is what a write answered: its result, and beside it the piece and the pending flag.
type writeAnswer struct {
	result, piece json.RawMessage
	pending       bool
}

// createdID picks the ID of the new piece of this kind ("listener", "pool", "health monitor",
// "member"): the answer's piece, else the step of that kind.
func createdID(ans *writeAnswer, kind string) (string, error) {
	var piece struct {
		ID string `json:"id"`
	}
	if len(ans.piece) > 0 && json.Unmarshal(ans.piece, &piece) == nil && piece.ID != "" {
		return piece.ID, nil
	}
	var out struct {
		Steps []lbStep `json:"steps"`
	}
	if err := json.Unmarshal(ans.result, &out); err != nil {
		return "", fmt.Errorf("load balancer %s create: unexpected answer (%w)", kind, err)
	}
	for _, s := range out.Steps {
		if s.Kind == kind && s.ID != "" && (s.Created || s.Applied) {
			return s.ID, nil
		}
	}
	return "", fmt.Errorf("load balancer %s create: %w", kind, errNoID)
}

// errNoID: the answer didn't name the new piece. The plugin leaves `piece` out unless the load
// balancer had settled when it answered (guide 9.6), so the caller looks the piece up instead.
var errNoID = errors.New("the answer names no new piece")

// newPieceID is the ID of a piece just made: from the answer, or else found on the load balancer,
// which lbWrite has already waited for, by what makes it unique (find).
func (c *Client) newPieceID(ctx context.Context, networkID int64, lbID, kind string, ans *writeAnswer, find func(*LoadBalancer) string) (string, error) {
	id, err := createdID(ans, kind)
	if !errors.Is(err, errNoID) {
		return id, err
	}
	lb, _, err := c.GetLoadBalancer(ctx, networkID, lbID, "")
	if err != nil {
		return "", fmt.Errorf("load balancer %s create: the %s was made, but reading it back failed: %w", kind, kind, err)
	}
	if find == nil {
		return "", fmt.Errorf("load balancer %s create: the answer names no new %s", kind, kind)
	}
	if id = find(lb); id == "" {
		return "", fmt.Errorf("load balancer %s create: the answer names no new %s, and the load balancer doesn't show one", kind, kind)
	}
	return id, nil
}

// pieceCreate runs a piece create and returns the new piece's ID. When the write fails after the
// plugin made the piece (the load balancer went to ERROR, or time ran out while waiting), the ID is
// still looked up and returned with the error, so the resource keeps it in state instead of
// leaving it behind for the next create to trip over.
func (c *Client) pieceCreate(ctx context.Context, networkID int64, lbID, action, kind string, in map[string]any, find func(*LoadBalancer) string) (string, error) {
	ans, err := c.lbWrite(ctx, networkID, lbID, action, in)
	if ans == nil {
		return "", err
	}
	id, idErr := c.newPieceID(ctx, networkID, lbID, kind, ans, find)
	if err != nil {
		return id, err
	}
	return id, idErr
}

func poolOf(lb *LoadBalancer, poolID string) *LBPool {
	for i := range lb.Pools {
		if lb.Pools[i].ID == poolID {
			return &lb.Pools[i]
		}
	}
	return nil
}

// lbPending reports a provisioning status that will still change.
func lbPending(status string) bool { return strings.HasPrefix(strings.ToUpper(status), "PENDING") }

// WaitLoadBalancer reads the load balancer until it is no longer PENDING_*, inside ctx. ERROR is
// an error that says so, with the load balancer returned.
func (c *Client) WaitLoadBalancer(ctx context.Context, networkID int64, id, after string) (*LoadBalancer, error) {
	return c.waitLB(ctx, networkID, id, after, false)
}

// waitLB is WaitLoadBalancer for a write the plugin answered with pending: its save was still
// running, so the load balancer may read ACTIVE, as it was, before the change reaches the cloud.
// Then ACTIVE counts only after a PENDING_* read, or once LBPendingGrace has passed without one.
func (c *Client) waitLB(ctx context.Context, networkID int64, id, after string, pending bool) (*LoadBalancer, error) {
	seen, trustAt := !pending, time.Now().Add(c.cfg.LBPendingGrace)
	lb, err := poll(ctx, c, func(ctx context.Context) (*LoadBalancer, error) {
		lb, _, err := c.GetLoadBalancer(ctx, networkID, id, "")
		return lb, err
	}, func(lb *LoadBalancer, err error) (bool, error) {
		if err != nil {
			return false, err
		}
		switch {
		case lb.ProvisioningStatus == "": // no status yet is not settled
			return false, nil
		case lbPending(lb.ProvisioningStatus):
			seen = true
			return false, nil
		}
		return seen || !time.Now().Before(trustAt), nil
	})
	if err != nil {
		return lb, fmt.Errorf("waiting for load balancer %s after the %s: %w", id, after, err)
	}
	if strings.EqualFold(lb.ProvisioningStatus, "ERROR") {
		return lb, &LBError{Action: after, Msg: fmt.Sprintf("Load balancer %s is in ERROR after the %s. The cloud did not say why; "+
			"look at it in the console (Network > Load Balancers), then run apply again.", id, after)}
	}
	return lb, nil
}

// CreateLoadBalancer creates an empty load balancer and waits until it is ACTIVE. requestID makes a
// retry find the one already made instead of making another: give the same one to every retry of
// one create. A load balancer that ends in ERROR is returned with the error, so its ID is kept.
func (c *Client) CreateLoadBalancer(ctx context.Context, networkID int64, requestID string, s LoadBalancerSpec) (*LoadBalancer, error) {
	spec := map[string]any{"name": s.Name, "description": s.Description, "vipSubnetId": s.VIPSubnetID,
		"listeners": []any{}, "pools": []any{}, "tags": nonNil(s.Tags)}
	if s.VIPAddress != "" {
		spec["vipAddress"] = s.VIPAddress // never "" or null: omitted lets the cloud choose
	}
	raw, err := c.runLB(ctx, "create", map[string]any{"networkId": itoa(networkID), "requestId": requestID, "spec": spec})
	c.lbChanged(networkID)
	if err != nil {
		return nil, err
	}
	var out struct {
		LoadBalancer *LoadBalancer `json:"loadbalancer"`
	}
	if err := json.Unmarshal(raw, &out); err != nil || out.LoadBalancer == nil || out.LoadBalancer.ID == "" {
		return nil, fmt.Errorf("load balancer create: the answer has no load balancer")
	}
	if !lbPending(out.LoadBalancer.ProvisioningStatus) && !strings.EqualFold(out.LoadBalancer.ProvisioningStatus, "ERROR") {
		return out.LoadBalancer, nil
	}
	lb, err := c.WaitLoadBalancer(ctx, networkID, out.LoadBalancer.ID, "create")
	if lb == nil {
		lb = out.LoadBalancer
	}
	return lb, err
}

// UpdateLoadBalancer changes the load balancer's own fields (loadbalancer-update) and waits for
// it to be ACTIVE again. Its listeners and pools are left as they are.
func (c *Client) UpdateLoadBalancer(ctx context.Context, networkID int64, lbID string, s LoadBalancerUpdate) error {
	_, err := c.lbWrite(ctx, networkID, lbID, "loadbalancer-update", map[string]any{"spec": s.body()})
	return err
}

// DeleteLoadBalancer deletes a load balancer, with everything on it, and waits until it is gone.
// One already gone is not an error.
func (c *Client) DeleteLoadBalancer(ctx context.Context, networkID int64, id string) error {
	unlock, err := c.lbLock(ctx, id)
	if err != nil {
		return err
	}
	defer unlock()
	_, err = c.runLB(ctx, "delete", map[string]any{"networkId": itoa(networkID), "lbId": id})
	c.lbChanged(networkID)
	if err != nil && !IsLBMissing(err) {
		return err
	}
	_, err = poll(ctx, c, func(ctx context.Context) (*LoadBalancer, error) {
		lb, _, err := c.GetLoadBalancer(ctx, networkID, id, "")
		return lb, err
	}, func(_ *LoadBalancer, err error) (bool, error) {
		if IsLBMissing(err) {
			return true, nil
		}
		return false, err
	})
	if err != nil {
		return fmt.Errorf("waiting for load balancer %s to be deleted: %w", id, err)
	}
	return nil
}

// lbWrite runs one piece action on a load balancer, under its lock, then waits until the load
// balancer is ACTIVE again. It returns what the action answered.
func (c *Client) lbWrite(ctx context.Context, networkID int64, lbID, action string, in map[string]any) (*writeAnswer, error) {
	unlock, err := c.lbLock(ctx, lbID)
	if err != nil {
		return nil, err
	}
	defer unlock()
	in["networkId"], in["lbId"] = itoa(networkID), lbID
	env, err := c.runLBEnv(ctx, action, in)
	c.lbChanged(networkID)
	if err != nil {
		return nil, err
	}
	ans := &writeAnswer{result: env.Result, piece: env.Piece, pending: env.Pending}
	if _, err := c.waitLB(ctx, networkID, lbID, action, ans.pending); err != nil {
		return ans, err
	}
	c.lbChanged(networkID) // the waiting reads are not the state to keep
	return ans, nil
}

// lbRemove runs a piece delete. A piece or load balancer already gone counts as removed.
func (c *Client) lbRemove(ctx context.Context, networkID int64, lbID, action string, in map[string]any) error {
	_, err := c.lbWrite(ctx, networkID, lbID, action, in)
	if err != nil && (IsLBMissing(err) || isPieceGone(err)) {
		return nil
	}
	return err
}

// isPieceGone reports the plugin's refusal for an ID the load balancer doesn't have.
func isPieceGone(err error) bool {
	var e *LBError
	if !errors.As(err, &e) {
		return false
	}
	m := strings.ToLower(e.Msg)
	// The plugin's words for a piece that isn't there (LbTopology). "not found" alone is not one:
	// "Cloud with ID 5 not found" is a different problem.
	for _, gone := range []string{"is not on this load balancer", "is not in pool", "has no health monitor"} {
		if strings.Contains(m, gone) {
			return true
		}
	}
	return false
}

func (c *Client) CreateLBListener(ctx context.Context, networkID int64, lbID string, s LBListenerSpec) (string, error) {
	return c.pieceCreate(ctx, networkID, lbID, "listener-create", "listener", map[string]any{"spec": s.body()}, func(lb *LoadBalancer) string {
		for _, l := range lb.Listeners { // protocol and port are unique on a load balancer
			if l.Port == s.Port && strings.EqualFold(l.Protocol, s.Protocol) {
				return l.ID
			}
		}
		return ""
	})
}

func (c *Client) UpdateLBListener(ctx context.Context, networkID int64, lbID, id string, s LBListenerSpec) error {
	_, err := c.lbWrite(ctx, networkID, lbID, "listener-update", map[string]any{"listenerId": id, "spec": s.body()})
	return err
}

func (c *Client) DeleteLBListener(ctx context.Context, networkID int64, lbID, id string) error {
	return c.lbRemove(ctx, networkID, lbID, "listener-delete", map[string]any{"listenerId": id})
}

func (c *Client) CreateLBPool(ctx context.Context, networkID int64, lbID string, s LBPoolSpec) (string, error) {
	// A pool has nothing unique to find it by, so its ID comes from its step only.
	return c.pieceCreate(ctx, networkID, lbID, "pool-create", "pool", map[string]any{"spec": s.body()}, nil)
}

func (c *Client) UpdateLBPool(ctx context.Context, networkID int64, lbID, id string, s LBPoolSpec) error {
	_, err := c.lbWrite(ctx, networkID, lbID, "pool-update", map[string]any{"poolId": id, "spec": s.body()})
	return err
}

func (c *Client) DeleteLBPool(ctx context.Context, networkID int64, lbID, id string) error {
	return c.lbRemove(ctx, networkID, lbID, "pool-delete", map[string]any{"poolId": id})
}

func (c *Client) CreateLBMonitor(ctx context.Context, networkID int64, lbID, poolID string, s LBMonitorSpec) (string, error) {
	return c.pieceCreate(ctx, networkID, lbID, "monitor-create", "health monitor", map[string]any{"poolId": poolID, "spec": s.body()}, func(lb *LoadBalancer) string {
		if p := poolOf(lb, poolID); p != nil && p.HealthMonitor != nil { // a pool has at most one
			return p.HealthMonitor.ID
		}
		return ""
	})
}

func (c *Client) UpdateLBMonitor(ctx context.Context, networkID int64, lbID, poolID string, s LBMonitorSpec) error {
	_, err := c.lbWrite(ctx, networkID, lbID, "monitor-update", map[string]any{"poolId": poolID, "spec": s.body()})
	return err
}

func (c *Client) DeleteLBMonitor(ctx context.Context, networkID int64, lbID, poolID string) error {
	return c.lbRemove(ctx, networkID, lbID, "monitor-delete", map[string]any{"poolId": poolID})
}

func (c *Client) AddLBMember(ctx context.Context, networkID int64, lbID, poolID string, s LBMemberSpec) (string, error) {
	return c.pieceCreate(ctx, networkID, lbID, "member-add", "member", map[string]any{"poolId": poolID, "spec": s.body()}, func(lb *LoadBalancer) string {
		if p := poolOf(lb, poolID); p != nil {
			for _, m := range p.Members { // address and port are unique in a pool
				if m.Address == s.Address && m.Port == s.Port {
					return m.ID
				}
			}
		}
		return ""
	})
}

func (c *Client) UpdateLBMember(ctx context.Context, networkID int64, lbID, poolID, id string, s LBMemberSpec) error {
	_, err := c.lbWrite(ctx, networkID, lbID, "member-update", map[string]any{"poolId": poolID, "memberId": id, "spec": s.body()})
	return err
}

func (c *Client) RemoveLBMember(ctx context.Context, networkID int64, lbID, poolID, id string) error {
	return c.lbRemove(ctx, networkID, lbID, "member-remove", map[string]any{"poolId": poolID, "memberId": id})
}

// SubnetExternalID is a console subnet's ID in the cloud: what a load balancer's VIP subnet is.
func (c *Client) SubnetExternalID(ctx context.Context, subnetID int64) (string, error) {
	var resp struct {
		Subnet *struct {
			ExternalID string `json:"externalId"`
		} `json:"subnet"`
	}
	path := fmt.Sprintf("/api/subnets/%d", subnetID)
	if err := c.get(ctx, path, nil, &resp); err != nil {
		return "", err
	}
	if resp.Subnet == nil || resp.Subnet.ExternalID == "" {
		return "", &APIError{Method: http.MethodGet, Path: path, StatusCode: http.StatusNotFound}
	}
	return resp.Subnet.ExternalID, nil
}
