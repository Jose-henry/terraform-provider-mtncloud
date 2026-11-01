package validators

import "testing"

// MTN's rule: ^(?=.*[a-z])(?=.*[A-Z])(?=.*\d).{8,}$
func TestMixedPassword(t *testing.T) {
	for s, ok := range map[string]bool{
		"Passw0rd": true, "aB3aaaaa": true, "Files-pw1": true,
		"aB3aaaa": false, "password1": false, "PASSWORD1": false, "Password": false, "": false,
		"éB3aaaaa": true, "ÉÈÀ3aaaa": false, // ASCII ranges, as the form's [A-Z]
	} {
		if got := check(MixedPassword(8), s); got != ok {
			t.Errorf("MixedPassword(%q) valid=%v, want %v", s, got, ok)
		}
	}
}
