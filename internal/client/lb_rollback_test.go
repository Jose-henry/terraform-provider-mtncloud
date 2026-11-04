package client

import (
	"encoding/json"
	"strings"
	"testing"
)

// A failed save that took back what it created says so, and a create doesn't look for the piece.
func TestLBWrite_RolledBack(t *testing.T) {
	b, _ := json.Marshal(map[string]any{"success": false, "action": "pool-create", "error": "OpenStack refused the members.",
		"rolledBack": true})
	f := newLBFake(t, string(b))
	id, err := f.client(t, fastPoll).CreateLBPool(t.Context(), 114, "lb-1", LBPoolSpec{Protocol: "HTTP", Algorithm: "ROUND_ROBIN"})
	if id != "" || err == nil || !strings.Contains(err.Error(), "removed again, so the next apply starts clean") ||
		strings.Contains(err.Error(), "OpenStack") { // mtncloud:allow-cloud-name (checking it is scrubbed)
		t.Fatalf("id %q, err %v", id, err)
	}
	if f.runs.Load() != 1 {
		t.Errorf("%d runs: nothing to look up after a clean rollback", f.runs.Load())
	}
}

// What a failed save left behind is listed, and the create still looks for its piece by tag.
func TestLBWrite_LeftBehind(t *testing.T) {
	b, _ := json.Marshal(map[string]any{"success": false, "action": "listener-create", "error": "Port refused.", "rolledBack": true,
		"leftBehind": []any{map[string]any{"kind": "pool", "name": "web", "id": "pool-3", "error": "still in use"},
			map[string]any{"kind": "listener", "name": "http", "id": "l-4", "kept": true}}})
	f := newLBFake(t, string(b), lbReadAs("ACTIVE"))
	_, err := f.client(t, fastPoll).CreateLBListener(t.Context(), 114, "lb-1", LBListenerSpec{Protocol: "HTTP", Port: 80})
	if err == nil || !strings.Contains(err.Error(), `Left on the load balancer: pool "web" (pool-3): still in use; listener "http" (l-4), kept.`) ||
		strings.Contains(err.Error(), "starts clean") {
		t.Fatalf("err %v", err)
	}
	if f.runs.Load() != 2 {
		t.Errorf("%d runs, want the create and one read for its tag", f.runs.Load())
	}
}

// A create that answered pending can still be taken back: once settled, a piece that isn't there
// fails the create instead of being kept in state.
func TestCreateLBPool_PendingThenRolledBack(t *testing.T) {
	f := newLBFake(t,
		lbAnswerTop("pool-create", map[string]any{"success": true, "steps": []any{}},
			map[string]any{"pending": true, "piece": map[string]any{"id": "pool-7"}}),
		lbReadAs("PENDING_UPDATE"), lbReadAs("ACTIVE"), lbReadAs("ACTIVE"))
	id, err := f.client(t, fastPoll).CreateLBPool(t.Context(), 114, "lb-1", LBPoolSpec{Protocol: "HTTP", Algorithm: "ROUND_ROBIN"})
	if id != "" || err == nil || !strings.Contains(err.Error(), "removed the new pool again") {
		t.Fatalf("id %q, err %v", id, err)
	}
}

func TestListenerOnPort(t *testing.T) {
	lb := &LoadBalancer{Listeners: []LBListener{{ID: "l-1", Protocol: "HTTP", Port: 80}, {ID: "l-2", Protocol: "UDP", Port: 53}}}
	for _, c := range []struct {
		proto string
		port  int64
		want  string
	}{{"TCP", 80, "l-1"}, {"TERMINATED_HTTPS", 80, "l-1"}, {"UDP", 80, ""}, {"SCTP", 53, ""}, {"udp", 53, "l-2"}, {"HTTP", 53, ""}} {
		got := ""
		if l := lb.ListenerOnPort(c.proto, c.port); l != nil {
			got = l.ID
		}
		if got != c.want {
			t.Errorf("%s %d: got %q, want %q", c.proto, c.port, got, c.want)
		}
	}
}

// An L7 policy taken back after one of its rules failed is marked on its step only (live, t137).
func TestLBWrite_StepRolledBack(t *testing.T) {
	b, _ := json.Marshal(map[string]any{"success": false, "action": "l7policy-create", "error": "L7 rule 'x': bad escape",
		"steps": []any{map[string]any{"kind": "L7 rule", "name": "x", "applied": false, "error": "bad escape"},
			map[string]any{"kind": "L7 policy", "name": "p", "applied": true, "rolledBack": true, "id": "pol-1"}}})
	f := newLBFake(t, string(b))
	_, err := f.client(t, fastPoll).CreateLBL7Policy(t.Context(), 114, "lb-1", "l-1", LBL7PolicySpec{Action: "REJECT", Position: 1})
	if err == nil || !strings.Contains(err.Error(), "bad escape. What this save created was removed again, so the next apply starts clean") {
		t.Fatalf("err %v", err)
	}
	if f.runs.Load() != 1 {
		t.Errorf("%d runs: nothing to look up after a clean rollback", f.runs.Load())
	}
}

// A pending create whose piece is still there once settled keeps it, from the wait's own last read.
func TestCreateLBPool_PendingStillThere(t *testing.T) {
	settled := lbAnswer("read", map[string]any{"success": true, "canManage": true, "loadbalancer": map[string]any{
		"id": "lb-1", "provisioning_status": "ACTIVE", "pools": []any{map[string]any{"id": "pool-7"}}}})
	f := newLBFake(t,
		lbAnswerTop("pool-create", map[string]any{"success": true, "steps": []any{}},
			map[string]any{"pending": true, "piece": map[string]any{"id": "pool-7"}}),
		lbReadAs("PENDING_UPDATE"), settled)
	id, err := f.client(t, fastPoll).CreateLBPool(t.Context(), 114, "lb-1", LBPoolSpec{Protocol: "HTTP", Algorithm: "ROUND_ROBIN"})
	if err != nil || id != "pool-7" {
		t.Fatalf("id %q, err %v", id, err)
	}
	if f.runs.Load() != 3 {
		t.Errorf("%d runs, want the create and two reads: the wait's last read is the check", f.runs.Load())
	}
}

// A create whose piece settled in ERROR fails with the piece attached: its ID is kept, with no
// read to look for it (plugin, 2026-11-03).
func TestCreateLBL7Policy_InError(t *testing.T) {
	b, _ := json.Marshal(map[string]any{"success": false, "pending": false, "action": "l7policy-create", "existing": true,
		"piece": map[string]any{"id": "pol-9", "provisioning_status": "ERROR"},
		"error": "L7 rule PATH REGEX '^/x' is in ERROR: the load balancer could not apply it."})
	f := newLBFake(t, string(b))
	id, err := f.client(t, fastPoll).CreateLBL7Policy(t.Context(), 114, "lb-1", "l-1", LBL7PolicySpec{Action: "REJECT", Position: 1})
	if id != "pol-9" || err == nil || !strings.Contains(err.Error(), "is in ERROR") {
		t.Fatalf("id %q, err %v", id, err)
	}
	if f.runs.Load() != 1 {
		t.Errorf("%d runs, want 1: no retry and no read", f.runs.Load())
	}
}

// A policy update that left an L7 rule in ERROR succeeds with a warning, collected for the caller.
func TestUpdateLBL7Policy_Warning(t *testing.T) {
	f := newLBFake(t,
		lbAnswerTop("l7policy-update", map[string]any{"success": true, "steps": []any{}},
			map[string]any{"warning": "L7 rule 'old' on OpenStack is in ERROR."}), // mtncloud:allow-cloud-name (checking it is scrubbed)
		lbReadAs("ACTIVE"))
	ctx, w := WithLBWarnings(t.Context())
	if err := f.client(t, fastPoll).UpdateLBL7Policy(ctx, 114, "lb-1", "l-1", "pol-1", LBL7PolicySpec{Action: "REJECT", Position: 1}, false); err != nil {
		t.Fatal(err)
	}
	if got := w.List(); len(got) != 1 || got[0] != "L7 rule 'old' on the cloud is in ERROR." {
		t.Errorf("warnings %q", got)
	}
}

// A delete the load balancer undid fails once, without a retry.
func TestDeleteLBL7Policy_NotRemoved(t *testing.T) {
	b, _ := json.Marshal(map[string]any{"success": false, "action": "l7policy-delete",
		"error": "L7 policy 'api' was not removed: the load balancer could not apply the delete."})
	f := newLBFake(t, string(b))
	err := f.client(t, fastPoll).DeleteLBL7Policy(t.Context(), 114, "lb-1", "l-1", "pol-1")
	if err == nil || !strings.Contains(err.Error(), "was not removed") {
		t.Fatalf("err %v", err)
	}
	if f.runs.Load() != 1 {
		t.Errorf("%d runs, want 1", f.runs.Load())
	}
}

// A delete that ended with no answer may not have run: it is tried again, and the plugin answers
// alreadyGone when the first one went through (plugin 6c47249). Nothing is waited for after that.
func TestDeleteLBPool_RetriedAlreadyGone(t *testing.T) {
	gone := lbAnswerTop("pool-delete", map[string]any{"success": true, "error": "Pool pool-1 is not on this load balancer."},
		map[string]any{"alreadyGone": true, "pending": false})
	f := newLBFake(t, "", gone)
	if err := f.client(t, fastPoll).DeleteLBPool(t.Context(), 114, "lb-1", "pool-1"); err != nil {
		t.Fatal(err)
	}
	if f.runs.Load() != 2 {
		t.Errorf("%d runs, want the delete, its retry, and no read", f.runs.Load())
	}
}

// alreadyGone with no result still counts as deleted.
func TestDeleteLBListener_AlreadyGoneNoResult(t *testing.T) {
	b, _ := json.Marshal(map[string]any{"success": true, "action": "listener-delete", "alreadyGone": true, "pending": false})
	f := newLBFake(t, string(b))
	if err := f.client(t, fastPoll).DeleteLBListener(t.Context(), 114, "lb-1", "l-1"); err != nil {
		t.Fatal(err)
	}
	if f.runs.Load() != 1 {
		t.Errorf("%d runs, want 1", f.runs.Load())
	}
}

// A save refused because a read failed says retryable at the top level: it is tried again.
func TestUpdateLBPool_RefusedUnreadRetried(t *testing.T) {
	refused, _ := json.Marshal(map[string]any{"success": false, "action": "pool-update", "retryable": true,
		"error": "Refusing to save: could not read the current members of pool pool-1. Retry in a moment."})
	f := newLBFake(t, string(refused),
		lbAnswer("pool-update", map[string]any{"success": true, "steps": []any{}}), lbReadAs("ACTIVE"))
	if err := f.client(t, fastPoll).UpdateLBPool(t.Context(), 114, "lb-1", "pool-1", LBPoolSpec{Protocol: "HTTP", Algorithm: "ROUND_ROBIN"}); err != nil {
		t.Fatal(err)
	}
	if f.runs.Load() != 3 {
		t.Errorf("%d runs, want the refused save, its retry and a read", f.runs.Load())
	}
}
