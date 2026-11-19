package client

import (
	"strings"
	"testing"
)

// The load balancer's messages name a Docker cluster's hosts by the cloud's own code: customers
// read docker-worker (owner, 2026-11-19).
func TestLBCustomerTextHidesDockerHostRole(t *testing.T) {
	in := "Cluster group cluster:220:" + DockerHostRole + " is not one this load balancer can use."
	got := lbCustomerText(in)
	if strings.Contains(got, DockerHostRole) || !strings.Contains(got, "cluster:220:docker-worker") {
		t.Errorf("lbCustomerText(%q) = %q", in, got)
	}
}
