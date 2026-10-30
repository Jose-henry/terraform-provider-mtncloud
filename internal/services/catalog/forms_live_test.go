package catalog

import (
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/mtn/terraform-provider-mtncloud/internal/client"
)

// TestAccCatalogForms_MatchSpecs reads every catalog item's live order form and fails when MTN has
// changed it in a way the resources don't follow: an item gone, a field a resource sends gone, or a
// field now required with no argument and no default. Run it with the acceptance tests (TF_ACC=1
// and MTNCLOUD_API_* set): MTN changes forms without notice (owner, 2026-10-30). Reads only.
func TestAccCatalogForms_MatchSpecs(t *testing.T) {
	if os.Getenv("TF_ACC") == "" || os.Getenv("MTNCLOUD_API_URL") == "" {
		t.Skip("acceptance check: set TF_ACC=1 and MTNCLOUD_API_URL")
	}
	c, err := client.New(client.Config{URL: os.Getenv("MTNCLOUD_API_URL"), Token: os.Getenv("MTNCLOUD_API_TOKEN"),
		Username: os.Getenv("MTNCLOUD_API_USERNAME"), Password: os.Getenv("MTNCLOUD_API_PASSWORD"), Tenant: os.Getenv("MTNCLOUD_API_TENANT")})
	if err != nil {
		t.Fatal(err)
	}
	covered := map[int64]bool{}
	for _, s := range orderSpecs() {
		t.Run(s.typeName, func(t *testing.T) {
			form, err := c.CatalogForm(t.Context(), s.ref())
			if err != nil {
				t.Fatalf("%s: %v", s.item, err)
			}
			covered[form.ItemID] = true
			if !s.ref().Is(form.Name) {
				t.Errorf("item %s is now named %q: add the new name to the spec (aliases keep the old one)", s.code, form.Name)
			}
			keys := map[string]bool{}
			for _, f := range s.fields {
				keys[f.key] = true
				ff, ok := form.Fields[f.key]
				switch {
				case !ok:
					t.Errorf("%s: the form has no field %q (argument %s) any more", s.item, f.key, f.attr)
				case f.required && f.def == "" && !ff.Required && ff.RequireOn == "" && (f.key != "volumes" || s.rules.minRootGB == 0):
					// A disk size with a stated minimum is required here although the form pre-fills 10 GB.
					t.Errorf("%s: argument %s is required, the form's %q isn't", s.item, f.attr, f.key)
				case f.required && f.def == "" && ff.Default != nil && !strings.Contains(fmt.Sprint(ff.Default), "${") && scalarField(ff.Type):
					t.Errorf("%s: argument %s is required, but the form fills %q with %v", s.item, f.attr, f.key, ff.Default)
				}
			}
			for key, ff := range form.Fields {
				if keys[key] || key == "cloud" || ff.Hidden || ff.Default != nil || !ff.Required || ff.VisibleOn != "" {
					continue
				}
				t.Errorf("%s: the form requires %q (%s), which no argument sets", s.item, key, ff.Label)
			}
		})
	}
	items, err := c.CatalogItemTypes(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	var missing []string
	for _, it := range items {
		if !covered[it.ID] {
			missing = append(missing, it.Name+" ("+it.Code+")")
		}
	}
	slices.Sort(missing)
	t.Logf("catalog items with no order resource here (some have their own resource: project, NFS, SMB, MOS): %s", strings.Join(missing, "; "))
}
