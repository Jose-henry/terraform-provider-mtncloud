package normalize

import "testing"

func TestPortRange(t *testing.T) {
	for in, want := range map[string]string{
		"":          "",
		"*":         "",
		"22":        "22",
		" 22 ":      "22",
		"22-22":     "22",
		"8000-8080": "8000-8080",
		"1-65535":   "1-65535",
	} {
		if got := PortRange(in); got != want {
			t.Errorf("PortRange(%q) = %q, want %q", in, got, want)
		}
	}
}
