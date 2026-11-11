package client

import (
	"context"
	"net/url"
	"strings"
)

// NodeFailure is the step a node's build failed at, from its process history (GET /api/processes?serverId):
// the script or task ("kubeadm worker join"), the phase it ran in ("execute script") and the platform's
// own message ("bash: line 1: null: command not found"). Zero when no failed step is recorded.
type NodeFailure struct {
	Step    string // the script or task, as the layout names it; else the step's own name
	Phase   string // "execute script", "execute task", "Prepare Resources", ...
	Message string // the error: its last lines, secrets left out (as a catalog order's)
}

func (f NodeFailure) String() string {
	if f.Step == "" && f.Message == "" {
		return ""
	}
	s := f.Step
	if f.Phase != "" && !strings.EqualFold(f.Phase, f.Step) {
		s += " (" + f.Phase + ")"
	}
	if f.Message != "" {
		s += ": " + f.Message
	}
	return strings.TrimPrefix(s, ": ")
}

// NodeFailureOf reads a node's newest failed build step (QA cluster 177's worker: "kubeadm worker
// join", "bash: line 1: null: command not found", 2026-11-11).
func (c *Client) NodeFailureOf(ctx context.Context, serverID int64) (NodeFailure, error) {
	var out struct {
		Processes []struct {
			Status      string `json:"status"`
			DateCreated string `json:"dateCreated"`
			Error       string `json:"error"`
			Message     string `json:"message"`
			ProcessType struct {
				Name string `json:"name"`
			} `json:"processType"`
			Events []struct {
				Status      string `json:"status"`
				Description string `json:"description"`
				Error       string `json:"error"`
				Message     string `json:"message"`
				ProcessType struct {
					Name string `json:"name"`
				} `json:"processType"`
			} `json:"events"`
		} `json:"processes"`
	}
	q := url.Values{"serverId": {itoa(serverID)}, "max": {"10"}, "sort": {"dateCreated"}, "direction": {"desc"}}
	if err := c.get(ctx, "/api/processes", q, &out); err != nil {
		return NodeFailure{}, err
	}
	newest := -1
	for i, p := range out.Processes {
		if strings.EqualFold(p.Status, "failed") && (newest < 0 || p.DateCreated > out.Processes[newest].DateCreated) {
			newest = i
		}
	}
	if newest < 0 {
		return NodeFailure{}, nil
	}
	p := out.Processes[newest]
	for _, e := range p.Events {
		if strings.EqualFold(e.Status, "failed") {
			step := e.Description
			if step == "" {
				step = e.ProcessType.Name
			}
			return NodeFailure{Step: step, Phase: e.ProcessType.Name, Message: lastLines(firstOf(e.Error, e.Message), 3)}, nil
		}
	}
	return NodeFailure{Step: p.ProcessType.Name, Message: lastLines(firstOf(p.Error, p.Message), 3)}, nil
}

func firstOf(s ...string) string {
	for _, v := range s {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
