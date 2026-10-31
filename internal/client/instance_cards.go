package client

import (
	"context"
	"fmt"
	"net/url"
	"slices"
)

// Network cards of a running instance. The instance's own interfaces list carries no card IDs (null
// at create, and null for a card added by a resize), but ?details=true has each server's cards with
// their IDs and which is primary (live QA 2026-10-31). /api/servers is 403 for the customer role.

// Card is a network card on an instance's server.
type Card struct {
	ID        int64
	NetworkID int64
	Primary   bool
}

// InstanceCards returns the cards of a single-server instance, primary first. Cards on an instance
// with several servers aren't changed in place, so that is an error.
func (c *Client) InstanceCards(ctx context.Context, id int64) ([]Card, error) {
	var resp struct {
		Instance struct {
			ContainerDetails []struct {
				Server *struct {
					Interfaces []struct {
						ID      int64    `json:"id"`
						Network *wireRef `json:"network"`
						Primary bool     `json:"primaryInterface"`
					} `json:"interfaces"`
				} `json:"server"`
			} `json:"containerDetails"`
		} `json:"instance"`
	}
	if err := c.get(ctx, "/api/instances/"+itoa(id), url.Values{"details": {"true"}}, &resp); err != nil {
		return nil, err
	}
	details := resp.Instance.ContainerDetails
	if len(details) != 1 || details[0].Server == nil {
		return nil, fmt.Errorf("instance %d has %d servers; network cards are changed in place only on an instance with one", id, len(details))
	}
	var out []Card
	for _, n := range details[0].Server.Interfaces {
		// A card left out of a resize is removed, so one that can't be read is an error, never skipped.
		if n.Network == nil || n.ID == 0 {
			return nil, fmt.Errorf("instance %d reports a network card without its ID or network", id)
		}
		card := Card{ID: n.ID, NetworkID: n.Network.ID, Primary: n.Primary}
		if card.Primary {
			out = slices.Insert(out, 0, card)
		} else {
			out = append(out, card)
		}
	}
	if len(out) == 0 || !out[0].Primary {
		return nil, fmt.Errorf("instance %d reports no primary network card", id)
	}
	return out, nil
}

// Reconfigured is what a resize should leave: the plan, the volume count with none of the removed
// volumes left, and the networks the cards are on (nil when the cards weren't changed).
type Reconfigured struct {
	PlanID         int64
	Volumes        int
	RemovedVolumes []int64
	Sizes          map[int64]int64 // grown volumes: ID to the size in GB
	Networks       []int64
}

// WaitForReconfigure polls until a resize has settled as asked. Straight after the call the
// instance still reads as it was, so counts alone can't tell (a card swapped for another keeps the
// count, and a moved one keeps the set): the networks, in order, and the removed volumes are checked too.
func (c *Client) WaitForReconfigure(ctx context.Context, id int64, want Reconfigured) (*Instance, error) {
	return poll(ctx, c, func(ctx context.Context) (*Instance, error) { return c.GetInstance(ctx, id) },
		func(in *Instance, err error) (bool, error) {
			switch {
			case IsNotFound(err):
				return false, nil
			case err != nil:
				return false, err
			}
			if InstanceFailed(in.Status) {
				return true, nil
			}
			return instanceSettled(in.Status) && in.PlanID == want.PlanID && reconfigured(in, want), nil
		})
}

func reconfigured(in *Instance, want Reconfigured) bool {
	if len(in.Volumes) != want.Volumes {
		return false
	}
	for _, v := range in.Volumes {
		if slices.Contains(want.RemovedVolumes, v.ID) {
			return false
		}
		if size, ok := want.Sizes[v.ID]; ok && v.Size < size {
			return false
		}
	}
	if want.Networks == nil {
		return true
	}
	got := make([]int64, 0, len(in.Interfaces))
	for _, n := range in.Interfaces {
		got = append(got, n.NetworkID)
	}
	// In order: a card moved within the list is removed and added at the end, which keeps the set.
	return slices.Equal(got, want.Networks)
}
