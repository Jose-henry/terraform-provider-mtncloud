package cluster

import (
	"slices"
	"testing"

	"github.com/mtn/terraform-provider-mtncloud/internal/client"
)

// A layout is read back as its key by code, and by name when MTN re-created it with a new code.
func TestLayoutKey(t *testing.T) {
	for _, tc := range []struct {
		l    client.ClusterLayout
		want string
	}{
		{client.ClusterLayout{Code: "0f0e59cc-3f26-4017-9584-acc8b197c24e", Name: "renamed"}, "ha3"},
		{client.ClusterLayout{Code: "new-code", Name: "MTN Kubernetes HA (5)"}, "ha5"},
		{client.ClusterLayout{Name: "mtn kubernetes single-master"}, "single"},
		{client.ClusterLayout{Code: "x", Name: "HKS Kubernetes 1.35 Cluster on Ubuntu 24.04"}, ""},
	} {
		if got := layoutKey(tc.l); got != tc.want {
			t.Errorf("layoutKey(%+v) = %q, want %q", tc.l, got, tc.want)
		}
	}
}

// What is sent is what the plan holds: labels that have MK8S already are sent as given.
func TestWithMK8S(t *testing.T) {
	for _, tc := range []struct{ in, want []string }{
		{nil, []string{"MK8S"}},
		{[]string{"apps"}, []string{"MK8S", "apps"}},
		{[]string{"apps", "MK8S"}, []string{"apps", "MK8S"}},
		{[]string{"mk8s"}, []string{"mk8s"}},
	} {
		if got := withMK8S(tc.in); !slices.Equal(got, tc.want) {
			t.Errorf("withMK8S(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
