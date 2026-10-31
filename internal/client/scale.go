package client

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
)

// Changing the number of servers in place (live 2026-10-31, t105): the instance offers "Add Node"
// (PUT /api/instances/{id}/action?code=generic-add-node, empty body, one server per call, running
// in about 4 minutes) and each server "Remove Node" (PUT /api/containers/{id}/action?code=
// generic-remove-node, gone in seconds). The codes are read from the actions lists, not assumed.

type actionList struct {
	Actions []struct {
		Code string `json:"code"`
		Name string `json:"name"`
	} `json:"actions"`
}

// actionCode finds the action whose code ends with suffix ("-add-node", "-remove-node").
func (c *Client) actionCode(ctx context.Context, path, suffix, what string) (string, error) {
	var l actionList
	if err := c.do(ctx, http.MethodGet, path+"/actions", nil, nil, &l); err != nil {
		return "", err
	}
	for _, a := range l.Actions {
		if strings.HasSuffix(a.Code, suffix) {
			return a.Code, nil
		}
	}
	return "", fmt.Errorf("MTN Cloud offers no %s action here, so the number of servers can't change in place", what)
}

func (c *Client) runAction(ctx context.Context, path, code string) error {
	return c.do(ctx, http.MethodPut, path+"/action", url.Values{"code": {code}}, map[string]any{}, nil)
}

// ScaleInstance sets the number of servers of instance id to n in place: Add Node for each one
// missing, Remove Node for the newest ones over n. It returns once the instance has n servers and
// each is running, or with the error of a server that failed.
func (c *Client) ScaleInstance(ctx context.Context, id int64, n int) (*Instance, error) {
	in, err := c.GetInstance(ctx, id)
	if err != nil {
		return nil, err
	}
	have := len(in.ContainerIDs) // oldest first
	switch {
	case n > have:
		path := "/api/instances/" + itoa(id)
		code, err := c.actionCode(ctx, path, "-add-node", "Add Node")
		if err != nil {
			return in, err
		}
		for range n - have {
			if err := c.runAction(ctx, path, code); err != nil {
				return in, fmt.Errorf("adding a server: %w", err)
			}
		}
	case n < have:
		for _, cid := range slices.Backward(in.ContainerIDs[n:]) {
			path := "/api/containers/" + itoa(cid)
			code, err := c.actionCode(ctx, path, "-remove-node", "Remove Node")
			if err != nil {
				return in, err
			}
			if err := c.runAction(ctx, path, code); err != nil && !IsNotFound(err) {
				return in, fmt.Errorf("removing server %d: %w", cid, err)
			}
		}
	}
	return c.waitForServers(ctx, id, n)
}

// waitForServers polls until the instance has n servers and each has settled: running, or stopped,
// suspended or warning (a stopped VM's servers stay stopped). A failed server is an error. Each poll
// is one read of the instance with its servers' details: the instance itself stays "running" while
// a new server deploys (live QA 2026-10-31, t108: 435 s of "running" with a server "deploying").
// A server missing from the details is read on its own; a login that can't read it waits for the
// count and the instance's status only.
func (c *Client) waitForServers(ctx context.Context, id int64, n int) (*Instance, error) {
	var failed error
	var statuses map[int64]string
	in, err := poll(ctx, c, func(ctx context.Context) (*Instance, error) {
		in, st, err := c.instanceWithServers(ctx, id)
		statuses = st
		return in, err
	},
		func(in *Instance, err error) (bool, error) {
			if err != nil {
				return false, err
			}
			if len(in.ContainerIDs) != n {
				return false, nil
			}
			for _, cid := range in.ContainerIDs {
				s, ok := statuses[cid]
				if !ok {
					s, err = c.serverStatus(ctx, cid)
				}
				switch {
				case IsForbidden(err):
					if InstanceFailed(in.Status) {
						failed = fmt.Errorf("instance %d is %s", id, in.Status)
						return true, nil
					}
					return instanceSettled(in.Status), nil
				case IsNotFound(err):
					return false, nil
				case err != nil:
					return false, err
				case InstanceFailed(s):
					failed = fmt.Errorf("server %d of instance %d is %s", cid, id, s)
					return true, nil
				case !instanceSettled(s):
					return false, nil
				}
			}
			return true, nil
		})
	if err == nil {
		err = failed
	}
	return in, err
}

// instanceWithServers reads an instance with details=true, which adds each server's status
// (containerDetails), so one call covers them all.
func (c *Client) instanceWithServers(ctx context.Context, id int64) (*Instance, map[int64]string, error) {
	var resp struct {
		Instance json.RawMessage `json:"instance"`
	}
	if err := c.get(ctx, "/api/instances/"+itoa(id), url.Values{"details": {"true"}}, &resp); err != nil {
		return nil, nil, err
	}
	var w wireInstance
	var d struct {
		ContainerDetails []struct {
			ID     int64  `json:"id"`
			Status string `json:"status"`
		} `json:"containerDetails"`
	}
	if err := json.Unmarshal(resp.Instance, &w); err != nil {
		return nil, nil, fmt.Errorf("reading instance %d: %w", id, err)
	}
	_ = json.Unmarshal(resp.Instance, &d) // no details: each server is read on its own
	st := make(map[int64]string, len(d.ContainerDetails))
	for _, cd := range d.ContainerDetails {
		if cd.Status != "" {
			st[cd.ID] = cd.Status
		}
	}
	return w.instance(), st, nil
}

func (c *Client) serverStatus(ctx context.Context, cid int64) (string, error) {
	var resp struct {
		Container struct {
			Status string `json:"status"`
		} `json:"container"`
	}
	err := c.do(ctx, http.MethodGet, "/api/containers/"+itoa(cid), nil, nil, &resp)
	return resp.Container.Status, err
}
