package client

import (
	"net/http"
	"strings"
	"testing"
)

// A 403 is a role only when the API itself answered: MTN's web firewall sends an HTML page with a
// reference, and a network block sends an empty body (issue report A6, B-018 #5).
func TestForbiddenKinds(t *testing.T) {
	for _, tc := range []struct {
		name, body, want, not string
	}{
		{"role", `{"success":false,"msg":"Unauthorized"}`, "Your role does not allow this call", "firewall"},
		{"firewall", `<html><body><h1>Access Restricted</h1><p>Reference ID: <b>2bE-LH4e-J8xB</b></p></body></html>`,
			"web firewall blocked this request (reference 2bE-LH4e-J8xB)", "role"},
		{"firewall, no reference", `<html><title>Access restricted</title></html>`, "web firewall blocked this request,", "(reference"},
		{"empty", ``, "network or IP address is blocked", "role"},
		{"other html", `<html><body>Forbidden</body></html>`, "network or IP address is blocked", "role"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			msg := newAPIError(http.MethodGet, "/api/ping", http.StatusForbidden, []byte(tc.body)).Error()
			if !strings.Contains(msg, tc.want) || strings.Contains(msg, tc.not) {
				t.Errorf("got %q, want %q and not %q", msg, tc.want, tc.not)
			}
			if hint := ForbiddenHint([]byte(tc.body)); !strings.Contains(hint, tc.want) {
				t.Errorf("legacy hint %q, want %q", hint, tc.want)
			}
		})
	}
	if msg := newAPIError(http.MethodGet, "/api/x", http.StatusBadGateway, []byte("<html>Access Restricted</html>")).Error(); strings.Contains(msg, "firewall") {
		t.Errorf("a 502 page isn't a firewall block: %q", msg)
	}
}
