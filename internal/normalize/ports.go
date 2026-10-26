package normalize

import "strings"

// PortRange writes a port range the way users write it: a single port as "22", not "22-22".
// MTN Cloud already echoes single ports as "22"; this guards against the other form. Empty and
// "*" (all ports) become "".
func PortRange(s string) string {
	s = strings.TrimSpace(s)
	if s == "" || s == "*" {
		return ""
	}
	if lo, hi, ok := strings.Cut(s, "-"); ok && strings.TrimSpace(lo) == strings.TrimSpace(hi) {
		return strings.TrimSpace(lo)
	}
	return s
}
