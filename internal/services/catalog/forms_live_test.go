package catalog

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

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
				case f.required && f.def == "" && !ff.Required && ff.RequireOn == "" && f.key != "volumes":
					// The disk is always required here (owner, 2026-10-31), although some forms pre-fill 10 GB.
					t.Errorf("%s: argument %s is required, the form's %q isn't", s.item, f.attr, f.key)
				case f.required && f.def == "" && ff.Default != nil && !strings.Contains(fmt.Sprint(ff.Default), "${") && scalarField(ff.Type):
					t.Errorf("%s: argument %s is required, but the form fills %q with %v", s.item, f.attr, f.key, ff.Default)
				}
			}
			// The docs come from the snapshot: it must still say what the live form says.
			if snap := snapshotFor(s.typeName); snap != nil {
				for key, ff := range form.Fields {
					sf := snap.field(key)
					if sf == nil || sf.Required != ff.Required || sf.Hidden != ff.Hidden || sf.Locked != ff.Locked ||
						sf.RequireOn != ff.RequireOn || sf.VisibleOn != ff.VisibleOn || sf.Pattern != ff.Pattern ||
						sf.Help != strings.Join(strings.Fields(ff.Help), " ") {
						t.Errorf("%s: field %q changed since the snapshot (%s): run TestAccCatalogForms_Snapshot, then make docs", s.item, key, snap.Read)
					}
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

// snapOf is the snapshot form of a live order form. choices lists a select's names when the list is
// short and doesn't hang on another field's value.
func snapOf(t *testing.T, c *client.Client, s orderSpec, form *client.CatalogForm, fv url.Values) formSnap {
	t.Helper()
	out := formSnap{Item: form.Name, Code: s.code, Read: time.Now().Format("2006-01-02")}
	skip := map[string]bool{"plan": true, "cloud": true, "resourcepool": true, "environment": true, "layout": true, "layouts": true}
	for _, ff := range form.Fields {
		f := snapField{Name: ff.Name, Label: ff.Label, Type: ff.Type, Required: ff.Required, Hidden: ff.Hidden, Locked: ff.Locked,
			NoBlank: ff.NoBlank, RequireOn: ff.RequireOn, VisibleOn: ff.VisibleOn, Pattern: ff.Pattern, Placeholder: ff.Placeholder,
			Min: ff.Min, Max: ff.Max, Help: strings.Join(strings.Fields(ff.Help), " ")}
		if ff.Default != nil {
			f.Default = fmt.Sprint(ff.Default)
			if strings.HasPrefix(strings.TrimSpace(f.Default), "[") {
				f.Default = "[disk or network list]"
			}
		}
		// A "field:value" dependsOn is only when the field shows; its list is the same (2026-10-31).
		dep := ff.DependsOn == "" || ff.DependsOn == "cloud" || ff.DependsOn == "resourcepool" || strings.Contains(ff.DependsOn, ":")
		if (ff.Type == "select" || ff.Type == "radio") && ff.ID != 0 && dep && !skip[ff.Name] && !strings.HasPrefix(ff.Name, "group") {
			if opts, err := c.FormOptionsIn(t.Context(), ff.ID, fv); err == nil && len(opts) > 0 && len(opts) <= 25 {
				for _, o := range opts {
					f.Choices = append(f.Choices, o.Name)
				}
			}
		}
		out.Fields = append(out.Fields, f)
	}
	slices.SortFunc(out.Fields, func(a, b snapField) int { return strings.Compare(a.Name, b.Name) })
	return out
}

// TestAccCatalogForms_Snapshot writes forms/<type>.json from the live forms, for the docs. Reads
// only. Run it with MTNCLOUD_FORM_SNAPSHOT=1 (and TF_ACC=1, MTNCLOUD_API_*), then make docs.
func TestAccCatalogForms_Snapshot(t *testing.T) {
	if os.Getenv("TF_ACC") == "" || os.Getenv("MTNCLOUD_FORM_SNAPSHOT") == "" {
		t.Skip("set TF_ACC=1 and MTNCLOUD_FORM_SNAPSHOT=1 to rewrite the form snapshots")
	}
	c, fv := liveClient(t)
	for _, s := range orderSpecs() {
		form, err := c.CatalogForm(t.Context(), s.ref())
		if err != nil {
			t.Fatalf("%s: %v", s.item, err)
		}
		v := url.Values{"catalogItemTypeId": {strconv.FormatInt(form.ItemID, 10)}}
		for k, x := range fv {
			v[k] = x
		}
		b, err := json.MarshalIndent(snapOf(t, c, s, form, v), "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join("forms", s.typeName+".json"), append(b, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// liveClient is a client for the acceptance environment, and the form values plan lists need
// (the cloud and the test project, MTNCLOUD_ACC_PROJECT, default "admin").
func liveClient(t *testing.T) (*client.Client, url.Values) {
	t.Helper()
	c, err := client.New(client.Config{URL: os.Getenv("MTNCLOUD_API_URL"), Token: os.Getenv("MTNCLOUD_API_TOKEN"),
		Username: os.Getenv("MTNCLOUD_API_USERNAME"), Password: os.Getenv("MTNCLOUD_API_PASSWORD"), Tenant: os.Getenv("MTNCLOUD_API_TENANT")})
	if err != nil {
		t.Fatal(err)
	}
	project := os.Getenv("MTNCLOUD_ACC_PROJECT")
	if project == "" {
		project = "admin"
	}
	pl, err := c.PlacementForGroup(t.Context(), "MTNNG_CLOUD_AZ_1")
	if err != nil {
		t.Fatal(err)
	}
	pr, err := c.ProjectByNameAt(t.Context(), pl, project)
	if err != nil {
		t.Fatal(err)
	}
	pool := "pool-" + strconv.FormatInt(pr.ID, 10)
	return c, url.Values{"cloud": {strconv.FormatInt(pl.CloudID, 10)}, "resourcepool": {pool}, "config.customOptions.resourcepool": {pool}}
}
