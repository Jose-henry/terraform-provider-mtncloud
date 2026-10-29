package client

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"
)

// Answers shaped like the live probe of 2026-10-29 (dev-docs/api-research.md), with made-up IDs.
func lbAnswer(action string, result any) string {
	b, _ := json.Marshal(map[string]any{"success": true, "action": action, "result": result})
	return string(b)
}

func lbRefusal(action, msg string) string {
	b, _ := json.Marshal(map[string]any{"success": false, "action": action, "error": msg})
	return string(b)
}

func lbReadAs(status string) string {
	return lbAnswer("read", map[string]any{"success": true, "canManage": true,
		"loadbalancer": map[string]any{"id": "lb-1", "name": "web", "provisioning_status": status, "operating_status": "ONLINE"}})
}

func TestCreateLoadBalancer_WaitsForActive(t *testing.T) {
	f := newLBFake(t,
		lbAnswer("create", map[string]any{"success": true, "loadbalancer": map[string]any{"id": "lb-1", "provisioning_status": "PENDING_CREATE"}}),
		lbReadAs("PENDING_CREATE"),
		lbReadAs("ACTIVE"))
	lb, err := f.client(t, fastPoll).CreateLoadBalancer(t.Context(), 114, "req-0001", LoadBalancerSpec{Name: "web", VIPSubnetID: "sub-1"})
	if err != nil || lb.ID != "lb-1" || lb.ProvisioningStatus != "ACTIVE" {
		t.Fatalf("got %+v, %v", lb, err)
	}
	if f.runs.Load() != 3 {
		t.Errorf("%d runs, want create and two reads", f.runs.Load())
	}
	create := f.opts[0]
	spec, _ := create["spec"].(map[string]any)
	if create["requestId"] != "req-0001" || create["networkId"] != "114" {
		t.Errorf("inputs %v", create)
	}
	if _, has := spec["vipAddress"]; has {
		t.Error("an empty VIP address must be left out, not sent")
	}
	if l, _ := spec["listeners"].([]any); l == nil || len(l) != 0 {
		t.Errorf("listeners %v, want an empty list (the pieces are their own resources)", spec["listeners"])
	}
}

func TestCreateLoadBalancer_ErrorKeepsTheID(t *testing.T) {
	f := newLBFake(t,
		lbAnswer("create", map[string]any{"loadbalancer": map[string]any{"id": "lb-1", "provisioning_status": "PENDING_CREATE"}}),
		lbReadAs("ERROR"))
	lb, err := f.client(t, fastPoll).CreateLoadBalancer(t.Context(), 114, "req-0001", LoadBalancerSpec{Name: "web", VIPSubnetID: "sub-1"})
	var lbe *LBError
	if !errors.As(err, &lbe) || !strings.Contains(err.Error(), "in ERROR after the create") {
		t.Fatalf("err %v", err)
	}
	if lb == nil || lb.ID != "lb-1" {
		t.Errorf("lb %+v: the ID must come back so the resource is kept, and replaced", lb)
	}
}

func TestCreateLBPool_TakesTheIDFromItsStep(t *testing.T) {
	f := newLBFake(t,
		lbAnswer("pool-create", map[string]any{"success": true, "steps": []any{
			map[string]any{"kind": "health monitor", "name": "m", "applied": true, "created": true, "id": "hm-1"},
			map[string]any{"kind": "pool", "name": "web", "applied": true, "created": true, "id": "pool-1"},
		}}),
		lbReadAs("ACTIVE"))
	id, err := f.client(t, fastPoll).CreateLBPool(t.Context(), 114, "lb-1", LBPoolSpec{Protocol: "HTTP", Algorithm: "ROUND_ROBIN"})
	if err != nil || id != "pool-1" {
		t.Fatalf("id %q, err %v", id, err)
	}
	if f.opts[0]["lbId"] != "lb-1" || f.opts[0]["action"] != "pool-create" {
		t.Errorf("inputs %v", f.opts[0])
	}
}

func TestDeleteLoadBalancer_WaitsUntilGone(t *testing.T) {
	f := newLBFake(t,
		lbAnswer("delete", map[string]any{"success": true, "id": "lb-1", "deleting": true}),
		lbReadAs("PENDING_DELETE"),
		lbRefusal("read", "Load balancer lb-1 was not found on this network."))
	if err := f.client(t, fastPoll).DeleteLoadBalancer(t.Context(), 114, "lb-1"); err != nil {
		t.Fatal(err)
	}
	if f.runs.Load() != 3 {
		t.Errorf("%d runs, want delete and reads until gone", f.runs.Load())
	}
}

func TestDeleteLBListener_AlreadyGoneIsDone(t *testing.T) {
	f := newLBFake(t, lbRefusal("listener-delete", "Listener l-9 is not on this load balancer."))
	if err := f.client(t, fastPoll).DeleteLBListener(t.Context(), 114, "lb-1", "l-9"); err != nil {
		t.Fatalf("err %v, want a listener already gone to count as deleted", err)
	}
}

// The plugin's words for pieces already gone (LbTopology); "not found" alone is something else.
func TestIsPieceGone(t *testing.T) {
	for msg, want := range map[string]bool{
		"Listener l-9 is not on this load balancer.": true,
		"Member m-9 is not in pool pool-1.":          true,
		"Pool pool-1 has no health monitor.":         true,
		"L7 policy p-3 is not on listener l-1.":      true,
		"Pool pool-1 has no members of instance 42.": true,
		"Cloud with ID 5 not found":                  false,
		"Another save is in progress.":               false,
	} {
		if got := isPieceGone(&LBError{Msg: msg}); got != want {
			t.Errorf("%q: %v, want %v", msg, got, want)
		}
	}
}

// A monitor made by a write that then failed (the load balancer went to ERROR) is still found, so
// the resource keeps it in state rather than leaving it behind.
func TestCreateLBMonitor_FailedWriteStillReturnsTheID(t *testing.T) {
	inError := lbAnswer("read", map[string]any{"success": true, "loadbalancer": map[string]any{
		"id": "lb-1", "provisioning_status": "ERROR",
		"pools": []any{map[string]any{"id": "pool-1", "healthmonitor": map[string]any{"id": "hm-1"}}},
	}})
	f := newLBFake(t, lbAnswer("monitor-create", map[string]any{"success": true}), inError, inError)
	id, err := f.client(t, fastPoll).CreateLBMonitor(t.Context(), 114, "lb-1", "pool-1", LBMonitorSpec{Type: "HTTP"})
	if err == nil || id != "hm-1" {
		t.Fatalf("id %q, err %v: want the ID with the error", id, err)
	}
}

func TestLBWrite_DropsTheCachedRead(t *testing.T) {
	f := newLBFake(t, lbReadAs("ACTIVE"), lbAnswer("listener-update", map[string]any{"success": true, "steps": []any{}}), lbReadAs("ACTIVE"), lbReadAs("ACTIVE"))
	c := f.client(t, fastPoll)
	if _, _, err := c.LoadBalancerSnapshot(t.Context(), 114, "lb-1", ""); err != nil {
		t.Fatal(err)
	}
	if err := c.UpdateLBListener(t.Context(), 114, "lb-1", "l-1", LBListenerSpec{ConnectionLimit: ptr(int64(10))}); err != nil {
		t.Fatal(err)
	}
	before := f.runs.Load()
	if _, _, err := c.LoadBalancerSnapshot(t.Context(), 114, "lb-1", ""); err != nil {
		t.Fatal(err)
	}
	if f.runs.Load() != before+1 {
		t.Error("a read after a write came from the cache: data sources would see the old load balancer (B-021)")
	}
}

func TestLBLock_OneWriteAtATimePerLoadBalancer(t *testing.T) {
	c := newFakeAPI(t).client(t)
	unlock, err := c.lbLock(t.Context(), "lb-1")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	if _, err := c.lbLock(ctx, "lb-1"); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("err %v: a second write to the same load balancer must wait", err)
	}
	other, err := c.lbLock(t.Context(), "lb-2")
	if err != nil {
		t.Fatalf("another load balancer waited: %v", err)
	}
	other()
	unlock()
}

// The live plugin answers a monitor or member create with neither piece nor steps when the load
// balancer hadn't settled; the new piece is then found on the load balancer.
func TestCreatePieces_FoundOnTheLoadBalancerWhenTheAnswerHasNoID(t *testing.T) {
	settled := lbAnswer("read", map[string]any{"success": true, "loadbalancer": map[string]any{
		"id": "lb-1", "provisioning_status": "ACTIVE",
		"listeners": []any{map[string]any{"id": "l-9", "protocol": "TCP", "protocol_port": 80}, map[string]any{"id": "l-1", "protocol": "HTTP", "protocol_port": 80}},
		"pools": []any{map[string]any{"id": "pool-1", "healthmonitor": map[string]any{"id": "hm-1"},
			"members": []any{map[string]any{"id": "m-9", "address": "10.0.0.5", "protocol_port": 81}, map[string]any{"id": "m-1", "address": "10.0.0.5", "protocol_port": 80}}}},
	}})
	noID := func(action string) string {
		return lbAnswer(action, map[string]any{"success": true, "wait": map[string]any{}})
	}
	f := newLBFake(t, noID("monitor-create"), lbReadAs("ACTIVE"), settled,
		noID("member-add"), lbReadAs("ACTIVE"), settled,
		noID("listener-create"), lbReadAs("ACTIVE"), settled)
	c := f.client(t, fastPoll)
	if id, err := c.CreateLBMonitor(t.Context(), 114, "lb-1", "pool-1", LBMonitorSpec{Type: "HTTP"}); err != nil || id != "hm-1" {
		t.Errorf("monitor %q, %v", id, err)
	}
	if id, err := c.AddLBMember(t.Context(), 114, "lb-1", "pool-1", LBMemberSpec{Address: "10.0.0.5", Port: 80}); err != nil || id != "m-1" {
		t.Errorf("member %q, %v (matched on address and port)", id, err)
	}
	if id, err := c.CreateLBListener(t.Context(), 114, "lb-1", LBListenerSpec{Protocol: "HTTP", Port: 80}); err != nil || id != "l-1" {
		t.Errorf("listener %q, %v (matched on protocol and port)", id, err)
	}
}

// lbAnswerTop is a write answer with pending and piece beside result, as the plugin sends them.
func lbAnswerTop(action string, result any, top map[string]any) string {
	m := map[string]any{"success": true, "action": action, "result": result}
	for k, v := range top {
		m[k] = v
	}
	b, _ := json.Marshal(m)
	return string(b)
}

// A pending answer means the save hadn't reached the cloud: an ACTIVE read straight after is the
// load balancer as it was, so the wait goes on until it has seen the change start.
func TestLBWrite_PendingWaitsForTheSaveToReachTheCloud(t *testing.T) {
	f := newLBFake(t,
		lbAnswerTop("pool-update", map[string]any{"success": true, "steps": []any{}}, map[string]any{"pending": true}),
		lbReadAs("ACTIVE"), lbReadAs("PENDING_UPDATE"), lbReadAs("ACTIVE"))
	c := f.client(t, fastPoll, func(cfg *Config) { cfg.LBPendingGrace = time.Hour })
	if err := c.UpdateLBPool(t.Context(), 114, "lb-1", "pool-1", LBPoolSpec{Algorithm: "ROUND_ROBIN"}); err != nil {
		t.Fatal(err)
	}
	if f.runs.Load() != 4 {
		t.Errorf("%d runs, want the update and three reads: the first ACTIVE read came before the save", f.runs.Load())
	}
}

// With no PENDING_* read at all (the save was quick), ACTIVE is believed after LBPendingGrace.
func TestLBWrite_PendingTrustsActiveAfterTheGrace(t *testing.T) {
	f := newLBFake(t,
		lbAnswerTop("pool-update", map[string]any{"success": true, "steps": []any{}}, map[string]any{"pending": true}),
		lbReadAs("ACTIVE"))
	c := f.client(t, func(cfg *Config) {
		cfg.LBPendingGrace, cfg.PollMin, cfg.PollMax = 15*time.Millisecond, 10*time.Millisecond, 10*time.Millisecond
	})
	if err := c.UpdateLBPool(t.Context(), 114, "lb-1", "pool-1", LBPoolSpec{Algorithm: "ROUND_ROBIN"}); err != nil {
		t.Fatal(err)
	}
}

func TestCreateLBMonitor_TakesTheIDFromTheTopLevelPiece(t *testing.T) {
	f := newLBFake(t,
		lbAnswerTop("monitor-create", map[string]any{"success": true, "steps": []any{}}, map[string]any{"piece": map[string]any{"id": "hm-7"}}),
		lbReadAs("ACTIVE"))
	id, err := f.client(t, fastPoll).CreateLBMonitor(t.Context(), 114, "lb-1", "pool-1", LBMonitorSpec{Type: "HTTP"})
	if err != nil || id != "hm-7" {
		t.Fatalf("id %q, err %v", id, err)
	}
	if f.runs.Load() != 2 {
		t.Errorf("%d runs: with the piece in the answer there is nothing to look up", f.runs.Load())
	}
}

// Every piece create sends a requestId, and a retry of that create sends the same one, so the
// plugin answers with the piece the first try made (existing: true) instead of making another.
func TestCreateLBPool_RetrySendsTheSameRequestID(t *testing.T) {
	f := newLBFake(t, "", // the first run ended without an answer: it may have made the pool
		lbAnswerTop("pool-create", map[string]any{"success": true, "steps": []any{}},
			map[string]any{"existing": true, "piece": map[string]any{"id": "pool-7"}}),
		lbReadAs("ACTIVE"))
	id, err := f.client(t, fastPoll).CreateLBPool(t.Context(), 114, "lb-1", LBPoolSpec{Protocol: "HTTP", Algorithm: "ROUND_ROBIN"})
	if err != nil || id != "pool-7" {
		t.Fatalf("id %q, err %v", id, err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	first, _ := f.opts[0]["requestId"].(string)
	if first == "" || len(first) > 64 || strings.Trim(first, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_-") != "" {
		t.Errorf("requestId %q: want 1 to 64 of A-Za-z0-9_-", first)
	}
	if f.opts[1]["requestId"] != first {
		t.Errorf("the retry sent requestId %v, the first try %q", f.opts[1]["requestId"], first)
	}
	if again := newRequestID(); again == first {
		t.Error("two creates got the same requestId")
	}
}

// echoTag answers job execution n with a read whose pool carries the request tag the first run sent:
// the pool the plugin made, found only by its tag.
func echoTag(t *testing.T, f *lbFake, n int64) {
	f.route("GET", "/api/job-executions/"+itoa(31000+n), func(w http.ResponseWriter, _ *http.Request) {
		f.mu.Lock()
		req, _ := f.opts[0]["requestId"].(string)
		f.mu.Unlock()
		pool := map[string]any{"id": "pool-9", "tags": []any{"mtncloud-req-" + req}}
		other := map[string]any{"id": "pool-1", "tags": []any{"mtncloud-req-someone-else"}}
		_, _ = w.Write(lbJob(t, lbAnswer("read", map[string]any{"success": true, "canManage": true,
			"loadbalancer": map[string]any{"id": "lb-1", "provisioning_status": "ACTIVE", "pools": []any{other, pool}}})))
	})
}

// The answer names no pool: it is found on the load balancer by its request tag alone.
func TestCreateLBPool_FoundByItsRequestTag(t *testing.T) {
	f := newLBFake(t, lbAnswer("pool-create", map[string]any{"success": true, "steps": []any{}}), lbReadAs("ACTIVE"))
	echoTag(t, f, 3)
	id, err := f.client(t, fastPoll).CreateLBPool(t.Context(), 114, "lb-1", LBPoolSpec{Protocol: "HTTP", Algorithm: "ROUND_ROBIN"})
	if err != nil || id != "pool-9" {
		t.Fatalf("id %q, err %v", id, err)
	}
}

// Every run ended with no answer: the pool may still have been made, and is kept by its tag.
func TestCreateLBPool_NoAnswerStillFindsThePool(t *testing.T) {
	refused := lbRefusal("pool-create", "Another save of this load balancer is in progress. Try again in a moment.")
	f := newLBFake(t, "", refused)
	echoTag(t, f, 3)
	id, err := f.client(t, fastPoll).CreateLBPool(t.Context(), 114, "lb-1", LBPoolSpec{Protocol: "HTTP", Algorithm: "ROUND_ROBIN"})
	if err == nil || id != "pool-9" {
		t.Fatalf("id %q, err %v: want pool-9 with the error", id, err)
	}
}

// No answer for an L7 policy create: the policy is found under its listener by the request tag, so a
// second create can't stack a copy at the same position.
func TestCreateLBL7Policy_NoAnswerStillFindsThePolicy(t *testing.T) {
	refused := lbRefusal("l7policy-create", "Another save of this load balancer is in progress. Try again in a moment.")
	f := newLBFake(t, "", refused)
	f.route("GET", "/api/job-executions/"+itoa(31003), func(w http.ResponseWriter, _ *http.Request) {
		f.mu.Lock()
		req, _ := f.opts[0]["requestId"].(string)
		f.mu.Unlock()
		pol := map[string]any{"id": "l7-9", "tags": []any{"mtncloud-req-" + req}}
		other := map[string]any{"id": "l7-1", "tags": []any{"mtncloud-req-someone-else"}}
		l := map[string]any{"id": "lis-1", "l7policies_expanded": []any{other, pol}}
		_, _ = w.Write(lbJob(t, lbAnswer("read", map[string]any{"success": true, "canManage": true,
			"loadbalancer": map[string]any{"id": "lb-1", "provisioning_status": "ACTIVE", "listeners": []any{l}}})))
	})
	id, err := f.client(t, fastPoll).CreateLBL7Policy(t.Context(), 114, "lb-1", "lis-1", LBL7PolicySpec{Action: "REJECT", Position: 1})
	if err == nil || id != "l7-9" {
		t.Fatalf("id %q, err %v: want l7-9 with the error", id, err)
	}
}

// Taken out of the configuration: SNI references are sent as [], member health-check overrides as null.
func TestLBSpecBodies_Clearing(t *testing.T) {
	l := LBListenerSpec{}
	l.ClearSNI()
	if b, _ := json.Marshal(l.body()); string(b) != `{"sniContainerRefs":[]}` {
		t.Errorf("listener body %s", b)
	}
	m := LBMemberSpec{ClearMonitorAddress: true, ClearMonitorPort: true}
	if b, _ := json.Marshal(m.body()); string(b) != `{"monitorAddress":null,"monitorPort":null}` {
		t.Errorf("member body %s", b)
	}
	if b, _ := json.Marshal(LBMemberSpec{}.body()); string(b) != `{}` {
		t.Errorf("an empty member change sends %s, want nothing", b)
	}
}

func TestLBSpecBodies(t *testing.T) {
	none, pool := "", "pool-1"
	l := LBListenerSpec{DefaultPoolID: &none}
	l.ClearAllowedCIDRs()
	b := l.body()
	if v, has := b["defaultPoolId"]; !has || v != nil {
		t.Errorf("defaultPoolId %v (present %v), want an explicit null to remove it", v, has)
	}
	if v, _ := b["allowedCidrs"].([]string); v == nil || len(v) != 0 {
		t.Errorf("allowedCidrs %v, want an empty list", b["allowedCidrs"])
	}
	b = LBListenerSpec{Protocol: "HTTP", Port: 80, DefaultPoolID: &pool}.body()
	if b["protocol"] != "HTTP" || b["port"] != int64(80) || b["defaultPoolId"] != "pool-1" {
		t.Errorf("create body %v", b)
	}
	if _, has := b["connectionLimit"]; has {
		t.Error("an unset field must be left out, so the update keeps it")
	}
	p := LBPoolSpec{SessionPersistence: &none}.body()
	if v, has := p["sessionPersistence"]; !has || v != nil {
		t.Errorf("sessionPersistence %v, want null to remove it", v)
	}
	u := LoadBalancerUpdate{Description: &none, Tags: &[]string{}}.body()
	if u["description"] != "" {
		t.Errorf("description %v, want \"\" to clear it", u["description"])
	}
	if v, _ := u["tags"].([]string); v == nil || len(v) != 0 {
		t.Errorf("tags %v, want an empty list to remove them", u["tags"])
	}
	for _, k := range []string{"name", "adminStateUp"} {
		if _, has := u[k]; has {
			t.Errorf("%s sent, but unset fields must be left out", k)
		}
	}
}

func ptr[T any](v T) *T { return &v }
