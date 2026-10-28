package catalog

import "testing"

func TestMadeBy(t *testing.T) {
	for _, c := range []struct {
		instance, order string
		want            bool
	}{
		{"web", "web", true},
		{"WEB", "web", true},
		{"web-1", "web", true},
		{"web-12", "web", true},
		{"web-db", "web", false},
		{"web_1", "web", false},
		{"web-", "web", false},
		{"web-1a", "web", false},
		{"webx", "web", false},
		{"we", "web", false},
	} {
		if got := madeBy(c.instance, c.order); got != c.want {
			t.Errorf("madeBy(%q, %q) = %v, want %v", c.instance, c.order, got, c.want)
		}
	}
}
