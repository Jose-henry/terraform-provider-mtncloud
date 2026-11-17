package catalog

import (
	"encoding/json"
	"testing"

	"github.com/mtn/terraform-provider-mtncloud/internal/client"
)

// Every resource orders its item under MTN Cloud's name now, from the names table (catalog read
// with the CLI, 2026-11-17), so messages and docs never show an old name.
func TestOrderSpecs_UseCurrentNames(t *testing.T) {
	for _, s := range orderSpecs() {
		n, ok := client.CatalogNames[s.code]
		if !ok {
			t.Errorf("%s: code %q isn't in client.CatalogNames", s.typeName, s.code)
			continue
		}
		if s.item != n.Name {
			t.Errorf("%s: item %q, want MTN's name %q", s.typeName, s.item, n.Name)
		}
		if snap, err := formFiles.ReadFile("forms/" + s.typeName + ".json"); err == nil && !containsItem(snap, n.Name) {
			t.Errorf("%s: forms/%s.json doesn't name the item %q", s.typeName, s.typeName, n.Name)
		}
	}
	for _, r := range []client.CatalogItemRef{projectItem, nfsItem, smbItem, client.MOSItem} {
		if r.Name() != client.CatalogNames[r.Code].Name {
			t.Errorf("%s: %q", r.Code, r.Name())
		}
	}
}

func containsItem(snap []byte, name string) bool {
	var s formSnap
	return json.Unmarshal(snap, &s) == nil && s.Item == name
}
