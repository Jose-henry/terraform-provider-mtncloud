package client

import "testing"

func TestPinnedMember(t *testing.T) {
	group := instanceGroupTag(301)
	p := &LBPool{Members: []LBMember{
		{ID: "g1", Address: "10.0.0.31", Port: 8080, Tags: []string{"mtncloud-req-1", group}},
		{ID: "p1", Address: "10.0.0.31", Port: 9090, Tags: []string{"mtncloud-req-2"}},
		{ID: "x1", Address: "10.0.0.40", Port: 8080},
	}}
	cases := []struct {
		addr string
		port int64
		want string
	}{
		{"10.0.0.31", 9090, "p1"},
		{"10.0.0.31", 8080, ""}, // that address on that port is a group member, not a pinned node
		{"10.0.0.31", 0, "p1"},  // any port (import): still never the group's
		{"10.0.0.40", 8080, "x1"},
		{"10.0.0.99", 8080, ""},
	}
	for _, c := range cases {
		got := ""
		if m := PinnedMember(p, c.addr, c.port); m != nil {
			got = m.ID
		}
		if got != c.want {
			t.Errorf("PinnedMember(%s, %d) = %q, want %q", c.addr, c.port, got, c.want)
		}
	}
}
