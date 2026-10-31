package client

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
)

// The shape of ?details=true on a single-server instance (live QA 2026-10-31, scrubbed): the cards
// with IDs are under containerDetails[].server.interfaces, eth0 marked primary.
const cardsDetails = `{"instance":{"id":9701,"interfaces":[{"id":null,"network":{"id":9301}}],
 "containerDetails":[{"id":9801,"server":{"id":9831,"interfaces":[
  {"id":9821,"name":"eth1","primaryInterface":false,"network":{"id":9302,"name":"db-net"}},
  {"id":9820,"name":"eth0","primaryInterface":true,"network":{"id":9301,"name":"app-net"}}]}}]}}`

func TestInstanceCards(t *testing.T) {
	f := newFakeAPI(t)
	f.route("GET", "/api/instances/9701", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("details") != "true" {
			t.Errorf("query %q: the card IDs are only in the details", r.URL.RawQuery)
		}
		_, _ = w.Write([]byte(cardsDetails))
	})
	cards, err := f.client(t).InstanceCards(t.Context(), 9701)
	if err != nil {
		t.Fatal(err)
	}
	want := []Card{{ID: 9820, NetworkID: 9301, Primary: true}, {ID: 9821, NetworkID: 9302}}
	if len(cards) != 2 || cards[0] != want[0] || cards[1] != want[1] {
		t.Errorf("cards %+v, want %+v", cards, want)
	}
}

// A card that can't be read is an error: a resize would remove a card left out.
func TestInstanceCards_Unreadable(t *testing.T) {
	f := newFakeAPI(t)
	bad := strings.Replace(cardsDetails, `{"id":9821,`, `{"id":null,`, 1)
	f.route("GET", "/api/instances/9701", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(bad)) })
	if _, err := f.client(t).InstanceCards(t.Context(), 9701); err == nil {
		t.Error("a card without its ID was skipped")
	}
}

// Cards of an instance with several servers aren't changed in place.
func TestInstanceCards_SeveralServers(t *testing.T) {
	f := newFakeAPI(t)
	two := strings.Replace(cardsDetails, `"containerDetails":[`, `"containerDetails":[{"id":9802,"server":{"id":9832,"interfaces":[]}},`, 1)
	f.route("GET", "/api/instances/9701", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(two)) })
	if _, err := f.client(t).InstanceCards(t.Context(), 9701); err == nil || !strings.Contains(err.Error(), "2 servers") {
		t.Errorf("err %v, want one about the 2 servers", err)
	}
}

// The wait for a resize doesn't end while a removed volume is still there, or while the cards are
// on other networks, even when the counts already match (a card swapped for another).
func TestWaitForReconfigure(t *testing.T) {
	body := string(instanceFixture(t, "get.json")) // plan 9402, volumes 9810 + 9811, cards on 9301 + 9302
	for name, tc := range map[string]struct {
		before string
		want   Reconfigured
	}{
		"volume removed": {body, Reconfigured{PlanID: 9402, Volumes: 2, RemovedVolumes: []int64{9811}}},
		"card swapped":   {body, Reconfigured{PlanID: 9402, Volumes: 2, Networks: []int64{9301, 9303}}},
		// The same networks in another order: the set already matches before the resize shows.
		"card moved": {strings.Replace(body, `{ "id": "9821", "row": 1, "network": { "id": 9302, "name": "db-net" }, "ipAddress": null, "primaryInterface": false },`,
			`{ "id": "9822", "row": 1, "network": { "id": 9303, "name": "web-net" }, "ipAddress": null, "primaryInterface": false },
      { "id": "9821", "row": 2, "network": { "id": 9302, "name": "db-net" }, "ipAddress": null, "primaryInterface": false },`, 1),
			Reconfigured{PlanID: 9402, Volumes: 2, Networks: []int64{9301, 9302, 9303}}},
		"disk grown": {body, Reconfigured{PlanID: 9402, Volumes: 2, Sizes: map[int64]int64{9811: 150}}},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFakeAPI(t)
			after := body
			if tc.want.RemovedVolumes != nil {
				after = strings.Replace(after, `"id": 9811, "name": "data-1"`, `"id": 9812, "name": "data-2"`, 1)
			}
			if tc.want.Sizes != nil {
				after = strings.Replace(after, `"name": "data-1", "size": 100`, `"name": "data-1", "size": 150`, 1)
			}
			switch len(tc.want.Networks) {
			case 2:
				after = strings.Replace(after, `"id": 9302, "name": "db-net"`, `"id": 9303, "name": "web-net"`, 1)
			case 3:
				after = strings.Replace(after, `"primaryInterface": false }`,
					`"primaryInterface": false },
      { "id": "9822", "row": 2, "network": { "id": 9303, "name": "web-net" }, "ipAddress": null, "primaryInterface": false }`, 1)
			}
			var reads atomic.Int32
			f.route("GET", "/api/instances/9701", func(w http.ResponseWriter, _ *http.Request) {
				if reads.Add(1) < 3 {
					_, _ = w.Write([]byte(tc.before))
					return
				}
				_, _ = w.Write([]byte(after))
			})
			if _, err := f.client(t, fastPoll).WaitForReconfigure(t.Context(), 9701, tc.want); err != nil {
				t.Fatal(err)
			}
			if n := reads.Load(); n < 3 {
				t.Errorf("returned after %d reads, before the change showed", n)
			}
		})
	}
}

// Data volumes are in the order they were made, not MTN Cloud's display slots: a disk added after
// one was removed takes the free slot (live QA 2026-10-31), and must still read as the last.
func TestInstanceVolumesInIDOrder(t *testing.T) {
	var w wireInstance
	body := `{"volumes":[{"id":9482,"size":5},{"id":9477,"name":"root","size":20,"rootVolume":true},{"id":9481,"size":10},{"id":-1,"size":1}]}`
	if err := json.Unmarshal([]byte(body), &w); err != nil {
		t.Fatal(err)
	}
	var ids []int64
	for _, v := range w.instance().Volumes {
		ids = append(ids, v.ID)
	}
	if fmt.Sprint(ids) != "[9477 9481 9482 -1]" {
		t.Errorf("volumes %v, want root, then by ID, then unsaved", ids)
	}
}
