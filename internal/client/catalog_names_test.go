package client

import (
	"strings"
	"testing"
)

// The names table is consistent: no name is used twice, current or former, so an old name can never
// match a different item; and a former name resolves to its item's current name.
func TestCatalogNames(t *testing.T) {
	seen := map[string]string{}
	for code, n := range CatalogNames {
		if n.Name == "" {
			t.Errorf("%s: no current name", code)
		}
		for _, name := range append([]string{n.Name}, n.Former...) {
			k := strings.ToLower(name)
			if other, dup := seen[k]; dup && other != code {
				t.Errorf("%q is used by %s and %s", name, other, code)
			}
			seen[k] = code
		}
		for _, f := range n.Former {
			if strings.EqualFold(f, n.Name) {
				t.Errorf("%s: %q is both current and former", code, f)
			}
			if c, cur, ok := RenamedCatalogItem(f); !ok || c != code || cur != n.Name {
				t.Errorf("RenamedCatalogItem(%q) = %s %q %v, want %s %q", f, c, cur, ok, code, n.Name)
			}
		}
		if _, _, ok := RenamedCatalogItem(n.Name); ok {
			t.Errorf("current name %q counts as renamed", n.Name)
		}
		if r := CatalogRef(code); r.Name() != n.Name || !r.Is(strings.ToUpper(n.Name)) {
			t.Errorf("CatalogRef(%s) = %+v", code, r)
		}
	}
	if c, cur, _ := RenamedCatalogItem("create project"); c != "001034sd" || cur != "Project" {
		t.Errorf("Create Project: %s %q", c, cur)
	}
}
