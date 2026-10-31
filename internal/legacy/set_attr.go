package legacy

import (
	"context"
	"log"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/hashicorp/terraform-plugin-sdk/v2/diag"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
)

// setFailures collects the attributes one CRUD call couldn't save. It lives in that call's context
// (withSetFailures), never in package state, so concurrent calls don't see each other's.
type setFailures struct {
	mu   sync.Mutex
	keys []string
}

type setFailuresKey struct{}

func withSetFailures(ctx context.Context) (context.Context, *setFailures) {
	f := &setFailures{}
	return context.WithValue(ctx, setFailuresKey{}, f), f
}

// setAttr is d.Set for the older resources. A value that doesn't fit the schema is a provider bug:
// before, the error was dropped and the attribute silently kept its old value. Now the attribute is
// named in a warning on the call (withPermissionHints adds it). It isn't an error, so a read that
// worked before still works (released provider, 2026-10-31). Only the key is reported: the SDK's
// error text contains the value, which may be a secret (the SDK logs the full error itself).
func setAttr(ctx context.Context, d *schema.ResourceData, key string, value any) {
	if d.Set(key, value) == nil {
		return
	}
	log.Printf("[WARN] couldn't save attribute %q", key)
	if f, ok := ctx.Value(setFailuresKey{}).(*setFailures); ok {
		f.mu.Lock()
		if !slices.Contains(f.keys, key) {
			f.keys = append(f.keys, key)
		}
		f.mu.Unlock()
	}
}

// setIntFromString saves a number the API returns as a string into a TypeInt attribute. An empty or
// non-numeric string leaves the attribute as it is, as before (the old d.Set failed silently).
func setIntFromString(ctx context.Context, d *schema.ResourceData, key, s string) {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil {
		log.Printf("[DEBUG] %s: the API returned no number, keeping the previous value", key)
		return
	}
	setAttr(ctx, d, key, n)
}

// warning turns the collected failures into one warning, or nothing.
func (f *setFailures) warning() diag.Diagnostics {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.keys) == 0 {
		return nil
	}
	return diag.Diagnostics{{
		Severity: diag.Warning,
		Summary:  "Some attributes couldn't be saved",
		Detail: "These attributes keep their previous values, because the value read from MTN Cloud doesn't fit them: " +
			strings.Join(f.keys, ", ") + ". This is a provider bug; please report it with the resource type.",
	}}
}
