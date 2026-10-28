package legacy

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	sdk "github.com/gomorpheus/morpheus-go-sdk"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
)

// cypherImporter imports a Cypher entry of one mount ("secret", "tfvars") by the numeric ID the
// console shows or by its key, with or without the mount ("app/db" or "secret/app/db"). Read finds
// the entry by key, so the key is what import must set.
func cypherImporter(mount string) schema.StateContextFunc {
	return func(_ context.Context, d *schema.ResourceData, meta interface{}) ([]*schema.ResourceData, error) {
		var list []sdk.Cypher
		if _, err := strconv.ParseInt(d.Id(), 10, 64); err == nil {
			resp, err := meta.(*sdk.Client).ListCyphers(&sdk.Request{QueryParams: map[string]string{"max": "10000"}})
			if err != nil {
				return nil, err
			}
			if r, ok := resp.Result.(*sdk.ListCypherResult); ok && r.Cyphers != nil {
				list = *r.Cyphers
			}
		}
		key, err := cypherImportKey(mount, d.Id(), list)
		if err != nil {
			return nil, err
		}
		if err := d.Set("key", key); err != nil {
			return nil, err
		}
		return []*schema.ResourceData{d}, nil
	}
}

// cypherImportKey turns an import ID into the key without its mount. A numeric ID is looked up in
// list, the Cypher entries the caller can see.
func cypherImportKey(mount, importID string, list []sdk.Cypher) (string, error) {
	prefix := mount + "/"
	n, err := strconv.ParseInt(importID, 10, 64)
	if err != nil {
		key := strings.TrimPrefix(importID, prefix)
		if key == "" {
			return "", fmt.Errorf("import ID %q has no key: use the Cypher ID or the key, for example %sapp/db", importID, prefix)
		}
		return key, nil
	}
	for _, c := range list {
		if c.ID != n {
			continue
		}
		if !strings.HasPrefix(c.ItemKey, prefix) {
			return "", fmt.Errorf("Cypher entry %d is %q, not in the %s mount", n, c.ItemKey, mount)
		}
		return strings.TrimPrefix(c.ItemKey, prefix), nil
	}
	return "", fmt.Errorf("no Cypher entry with ID %d is visible to this login", n)
}
