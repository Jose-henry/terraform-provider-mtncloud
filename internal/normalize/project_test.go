package normalize

import "testing"

func TestProjectShortName(t *testing.T) {
	cases := []struct {
		full, tenant, want string
	}{
		{"web-prod-Acme", "Acme", "web-prod"},       // the normal case
		{"admin", "Acme", "admin"},                  // shared pool, no suffix
		{"slowui", "Acme", "slowui"},                // shared pool, no suffix
		{"web-prod-Acme", "Other", "web-prod-Acme"}, // another tenant's suffix is not stripped
		{"Acme", "Acme", "Acme"},                    // no "-" before the tenant: not a suffix
		{"-Acme", "Acme", "-Acme"},                  // nothing would be left: keep the full name
		{"a-b-c-Acme", "Acme", "a-b-c"},             // dashes inside the name survive
		{"web-Acme-Acme", "Acme", "web-Acme"},       // strip the suffix once only
		{"web-prod-Acme", "", "web-prod-Acme"},      // tenant unknown: change nothing
		{"prod-MTNNG_MASTER_TENANT", "MTNNG_MASTER_TENANT", "prod"},

		// Case-sensitive (owner's decision): OpenStack names are, and two projects may differ only by case.
		{"web-prod-acme", "Acme", "web-prod-acme"},
		{"web-prod-ACME", "Acme", "web-prod-ACME"},
	}
	for _, c := range cases {
		if got := ProjectShortName(c.full, c.tenant); got != c.want {
			t.Errorf("ProjectShortName(%q, %q) = %q, want %q", c.full, c.tenant, got, c.want)
		}
	}
}

func TestProjectMatches(t *testing.T) {
	for _, c := range []struct {
		input, full string
		want        bool
	}{
		{"web-prod", "web-prod-Acme", true},
		{"web-prod-Acme", "web-prod-Acme", true},
		{"admin", "admin", true},
		{"web", "web-prod-Acme", false},
		{"prod", "web-prod-Acme", false},
		{"Web-Prod", "web-prod-Acme", false}, // case-sensitive
	} {
		if got := ProjectMatches(c.input, c.full, "Acme"); got != c.want {
			t.Errorf("ProjectMatches(%q, %q) = %v, want %v", c.input, c.full, got, c.want)
		}
	}
}
