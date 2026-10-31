package client

import (
	"context"
	"net/http"
	"slices"
)

// FillServerIPs sets in.PublicIPs: the floating IP of each of the instance's servers, in the order
// of ContainerIDs, "" for a server that has none. The instance shows only one address
// (connectionInfo), so with several servers each is read from its own container, where `ip` is the
// floating address and `internalIp` the private one (customer login, 2026-10-31, B-013). One server
// costs no call.
func (c *Client) FillServerIPs(ctx context.Context, in *Instance) error {
	if len(in.ContainerIDs) <= 1 {
		in.PublicIPs = nil
		if len(in.ContainerIDs) == 1 {
			in.PublicIPs = []string{in.PublicIP}
		}
		return nil
	}
	out := make([]string, len(in.ContainerIDs))
	for i, id := range in.ContainerIDs {
		var resp struct {
			Container struct {
				IP         string `json:"ip"`
				InternalIP string `json:"internalIp"`
			} `json:"container"`
		}
		if err := c.do(ctx, http.MethodGet, "/api/containers/"+itoa(id), nil, nil, &resp); err != nil {
			if IsForbidden(err) || IsNotFound(err) {
				// A role that can't read servers, or one going away: the instance's one address stands.
				in.PublicIPs = nil
				return nil
			}
			return err
		}
		if ip := resp.Container.IP; ip != "" && ip != resp.Container.InternalIP {
			out[i] = ip
		}
	}
	in.PublicIPs = out
	if in.PublicIP == "" {
		for _, ip := range out {
			if ip != "" {
				in.PublicIP = ip
				break
			}
		}
	}
	return nil
}

// MissingPublicIP reports whether a server of the instance has no floating IP. Before
// FillServerIPs, only the instance's one address is known.
func (in *Instance) MissingPublicIP() bool {
	if len(in.PublicIPs) > 1 {
		return slices.Contains(in.PublicIPs, "")
	}
	return in.PublicIP == ""
}

// NoPublicIP reports whether no server of the instance has a floating IP. With several servers
// read, only their own addresses count (connectionInfo may show another server's private one).
func (in *Instance) NoPublicIP() bool {
	if len(in.PublicIPs) > 1 {
		for _, ip := range in.PublicIPs {
			if ip != "" {
				return false
			}
		}
		return true
	}
	return in.PublicIP == ""
}
