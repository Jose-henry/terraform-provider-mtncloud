package client

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
)

// scaleFake is an instance (id 7) whose servers come and go with the Add Node and Remove Node
// actions. A new server reads "deploying" once, then "running" (or failStatus, when set).
type scaleFake struct {
	mu         sync.Mutex
	servers    []int64 // listed newest first, as MTN Cloud may
	next       int64
	reads      map[int64]int
	failStatus string
	noActions  bool
	forbidden  bool   // server reads answer 403, as for a role that can't read them
	busyReads  int    // the instance reads "provisioning" this many times before "running"
	instStatus string // when set, the instance's status once busyReads are used up
	details    bool   // the instance read with details=true lists each server's status, as MTN Cloud does
	removed    []int64
}

func newScaleFake(t *testing.T, f *fakeAPI, servers ...int64) *scaleFake {
	s := &scaleFake{servers: servers, next: 200, reads: map[int64]int{}}
	f.route("GET", "/api/instances/7", func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		status := "running"
		if s.instStatus != "" {
			status = s.instStatus
		}
		if s.busyReads > 0 {
			s.busyReads--
			status = "provisioning"
		}
		inst := map[string]any{"id": 7, "name": "web", "status": status, "containers": s.servers}
		if s.details && r.URL.Query().Get("details") == "true" {
			var ds []map[string]any
			for _, id := range s.servers {
				ds = append(ds, map[string]any{"id": id, "status": s.statusOf(id)})
			}
			inst["containerDetails"] = ds
		}
		b, _ := json.Marshal(map[string]any{"instance": inst})
		_, _ = w.Write(b)
	})
	f.route("GET", "/api/instances/7/actions", func(w http.ResponseWriter, _ *http.Request) {
		if s.noActions {
			_, _ = w.Write([]byte(`{"instanceIds":[7],"actions":[]}`))
			return
		}
		_, _ = w.Write([]byte(`{"instanceIds":[7],"actions":[{"code":"generic-add-node","name":"Add Node"}]}`))
	})
	f.route("PUT", "/api/instances/7/action", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("code") != "generic-add-node" {
			t.Errorf("add sent code %q", r.URL.Query().Get("code"))
		}
		s.mu.Lock()
		s.next++
		id := s.next
		s.servers = append([]int64{id}, s.servers...)
		s.mu.Unlock()
		s.serverRoutes(t, f, id)
		_, _ = w.Write([]byte(`{"success":true}`))
	})
	for _, id := range servers {
		s.serverRoutes(t, f, id)
		s.reads[id] = 1 // already running
	}
	return s
}

// statusOf is a server's status for one read (s.mu held): a new server reads "deploying" once.
func (s *scaleFake) statusOf(id int64) string {
	status := "running"
	if s.reads[id] == 0 {
		status = "deploying"
	} else if s.failStatus != "" {
		status = s.failStatus
	}
	s.reads[id]++
	return status
}

func (s *scaleFake) serverRoutes(t *testing.T, f *fakeAPI, id int64) {
	p := fmt.Sprintf("/api/containers/%d", id)
	f.route("GET", p, func(w http.ResponseWriter, _ *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.forbidden {
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"success":false,"msg":"Forbidden"}`))
			return
		}
		if !slices.Contains(s.servers, id) {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"success":false,"msg":"Container not found"}`))
			return
		}
		_, _ = fmt.Fprintf(w, `{"container":{"id":%d,"status":%q}}`, id, s.statusOf(id))
	})
	f.route("GET", p+"/actions", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprintf(w, `{"containerIds":[%d],"actions":[{"code":"generic-remove-node","name":"Remove Node"}]}`, id)
	})
	f.route("PUT", p+"/action", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("code") != "generic-remove-node" {
			t.Errorf("remove sent code %q", r.URL.Query().Get("code"))
		}
		s.mu.Lock()
		s.servers = slices.DeleteFunc(s.servers, func(x int64) bool { return x == id })
		s.removed = append(s.removed, id)
		s.mu.Unlock()
		_, _ = w.Write([]byte(`{"success":true}`))
	})
}

func TestScaleInstance(t *testing.T) {
	ctx := context.Background()

	t.Run("up", func(t *testing.T) {
		f := newFakeAPI(t)
		s := newScaleFake(t, f, 101)
		in, err := f.client(t, fastPoll).ScaleInstance(ctx, 7, 3)
		if err != nil {
			t.Fatal(err)
		}
		if n := f.count("PUT", "/api/instances/7/action"); n != 2 {
			t.Errorf("%d Add Node calls, want 2", n)
		}
		if !slices.Equal(in.ContainerIDs, []int64{101, 201, 202}) {
			t.Errorf("servers %v, want 3, oldest first", in.ContainerIDs)
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.reads[201] < 2 || s.reads[202] < 2 {
			t.Errorf("returned before the new servers ran: reads %v", s.reads)
		}
	})

	t.Run("down removes the newest", func(t *testing.T) {
		f := newFakeAPI(t)
		s := newScaleFake(t, f, 103, 101, 102)
		in, err := f.client(t, fastPoll).ScaleInstance(ctx, 7, 1)
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(in.ContainerIDs, []int64{101}) {
			t.Errorf("servers %v, want the oldest kept", in.ContainerIDs)
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		if !slices.Equal(s.removed, []int64{103, 102}) {
			t.Errorf("removed %v, want the newest, newest first", s.removed)
		}
	})

	t.Run("same count sends nothing", func(t *testing.T) {
		f := newFakeAPI(t)
		newScaleFake(t, f, 101, 102)
		if _, err := f.client(t, fastPoll).ScaleInstance(ctx, 7, 2); err != nil {
			t.Fatal(err)
		}
		if n := f.count("PUT", "/api/instances/7/action") + f.count("GET", "/api/instances/7/actions"); n != 0 {
			t.Errorf("%d action calls, want none", n)
		}
	})

	t.Run("no Add Node action", func(t *testing.T) {
		f := newFakeAPI(t)
		s := newScaleFake(t, f, 101)
		s.noActions = true
		_, err := f.client(t, fastPoll).ScaleInstance(ctx, 7, 2)
		if err == nil || !strings.Contains(err.Error(), "no Add Node action") {
			t.Errorf("err %v, want no Add Node action", err)
		}
	})

	t.Run("stopped servers are settled", func(t *testing.T) {
		f := newFakeAPI(t)
		s := newScaleFake(t, f, 101, 102)
		s.failStatus = "stopped" // a stopped VM: its servers stay stopped
		if _, err := f.client(t, fastPoll).ScaleInstance(ctx, 7, 1); err != nil {
			t.Errorf("scaling a stopped VM down: %v, want done", err)
		}
	})

	t.Run("servers can't be read", func(t *testing.T) {
		f := newFakeAPI(t)
		s := newScaleFake(t, f, 101)
		s.forbidden = true
		s.busyReads = 3 // the new server shows in the list while the instance still provisions
		in, err := f.client(t, fastPoll).ScaleInstance(ctx, 7, 2)
		if err != nil || len(in.ContainerIDs) != 2 {
			t.Errorf("forbidden server reads: %v, %v; want done once there are 2", err, in)
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.busyReads != 0 || in.Status != "running" {
			t.Errorf("returned while the instance was %q (%d busy reads left), want it settled", in.Status, s.busyReads)
		}
	})

	t.Run("server statuses from the instance details", func(t *testing.T) {
		// As live: the instance stays running while the new server deploys, and this login can't
		// read servers on their own.
		f := newFakeAPI(t)
		s := newScaleFake(t, f, 101)
		s.forbidden, s.details = true, true
		in, err := f.client(t, fastPoll).ScaleInstance(ctx, 7, 2)
		if err != nil || len(in.ContainerIDs) != 2 {
			t.Fatalf("%v, %v", err, in)
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.reads[201] < 2 {
			t.Errorf("returned while the new server was deploying: reads %v", s.reads)
		}
		if n := f.count("GET", "/api/containers/201"); n != 0 {
			t.Errorf("%d server reads, want none (the details cover them)", n)
		}
	})

	t.Run("servers can't be read and the instance fails", func(t *testing.T) {
		f := newFakeAPI(t)
		s := newScaleFake(t, f, 101)
		s.forbidden = true
		s.busyReads = 1
		s.instStatus = "failed"
		_, err := f.client(t, fastPoll).ScaleInstance(ctx, 7, 2)
		if err == nil || !strings.Contains(err.Error(), "is failed") {
			t.Errorf("err %v, want the failed instance reported", err)
		}
	})

	t.Run("a new server fails", func(t *testing.T) {
		f := newFakeAPI(t)
		s := newScaleFake(t, f, 101)
		s.failStatus = "failed"
		_, err := f.client(t, fastPoll).ScaleInstance(ctx, 7, 2)
		if err == nil || !strings.Contains(err.Error(), "is failed") {
			t.Errorf("err %v, want the failed server", err)
		}
	})
}
