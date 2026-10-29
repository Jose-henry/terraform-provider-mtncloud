package storage

import "testing"

func TestExpired(t *testing.T) {
	for until, want := range map[string]bool{
		"2020-01-01T00:00:00Z":      true,
		"2999-01-01T00:00:00+01:00": false,
		"":                          false,
		"not a date":                false,
	} {
		if got := expired(until); got != want {
			t.Errorf("expired(%q) = %v, want %v", until, got, want)
		}
	}
}
