package provider

import (
	"encoding/json"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/knownvalue"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/statecheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"
)

// archive is one archive's record, shaped like the live answers of 2026-10-29 (api-research.md).
func (m *mosFake) archive(name string) map[string]any {
	a := m.archMeta[name]
	if a == nil {
		m.next++
		a = map[string]any{"id": m.next, "description": "", "isPublic": false, "fileCount": 0, "rawSize": nil}
		m.archMeta[name] = a
	}
	sid := m.archives[name]
	sp := map[string]any{"id": sid}
	if rec := m.records[sid]; rec != nil {
		sp["name"] = rec["name"]
	}
	out := map[string]any{"name": name, "storageProvider": sp, "owner": map[string]any{"id": 42, "name": "Kestrel-Dev_01"},
		"visibility": "private", "code": "c0de",
		"filePath": "morpheus-archives/c0de/"} // mtncloud:allow-upstream-name (the platform's own archive prefix in the bucket, as the API answers it)
	for k, v := range a {
		out[k] = v
	}
	if n := m.archiveFileCount(name); n > 0 {
		out["fileCount"] = out["fileCount"].(int) + n
	}
	return out
}

func (m *mosFake) archiveByID(id int64) string {
	for name := range m.archives {
		if a := m.archMeta[name]; a != nil && a["id"] == id {
			return name
		}
	}
	return ""
}

// serveArchives is /api/archives/buckets, with m.mu held.
func (m *mosFake) serveArchives(w http.ResponseWriter, r *http.Request) {
	if strings.Contains(r.URL.Path, "/files") {
		m.serveArchiveFiles(w, r) // archive_file_test.go
		return
	}
	var body struct {
		ArchiveBucket map[string]any `json:"archiveBucket"`
	}
	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&body)
	}
	in := body.ArchiveBucket
	if r.URL.Path == "/api/archives/buckets" {
		if r.Method == http.MethodGet {
			rows := []any{}
			for name := range m.archives {
				rows = append(rows, m.archive(name))
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"archiveBuckets": rows, "meta": map[string]any{"total": len(rows)}})
			return
		}
		name, _ := in["name"].(string)
		sp, _ := in["storageProvider"].(map[string]any)
		sid, _ := sp["id"].(float64)
		switch {
		case m.archives[name] != 0:
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"success":false,"msg":"Name must be unique","errors":{"name":"must be unique"}}`))
			return
		case m.records[int64(sid)] == nil:
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"success":false,"errors":{"storageProvider":"not found"}}`))
			return
		case in["visibility"] != "private" || in["accounts"] == nil:
			w.WriteHeader(http.StatusBadRequest) // the provider always creates private archives, shared with no one
			return
		}
		m.archives[name] = int64(sid)
		a := m.archive(name)
		m.archMeta[name]["description"], m.archMeta[name]["isPublic"] = in["description"], in["isPublic"]
		a["description"], a["isPublic"] = in["description"], in["isPublic"]
		_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "archiveBucket": a})
		return
	}
	id, _ := strconv.ParseInt(strings.TrimPrefix(r.URL.Path, "/api/archives/buckets/"), 10, 64)
	name := m.archiveByID(id)
	switch {
	case name == "":
		w.WriteHeader(http.StatusNotFound)
	case r.Method == http.MethodGet:
		_ = json.NewEncoder(w).Encode(map[string]any{"archiveBucket": m.archive(name)})
	case r.Method == http.MethodPut:
		if _, has := in["storageProvider"]; has {
			w.WriteHeader(http.StatusBadRequest) // the bucket can't change
			return
		}
		meta := m.archMeta[name]
		if n, _ := in["name"].(string); n != "" && n != name {
			m.archives[n], m.archMeta[n] = m.archives[name], meta
			m.renameArchiveFiles(name, n)
			delete(m.archives, name)
			delete(m.archMeta, name)
		}
		meta["description"], meta["isPublic"] = in["description"], in["isPublic"]
		if meta["uploadOnPut"] == true {
			// Someone adds a file in the console while Terraform applies.
			n, _ := meta["fileCount"].(int)
			meta["fileCount"], meta["rawSize"] = n+1, 13*(n+1)
		}
		_, _ = w.Write([]byte(`{"success":true}`))
	case r.Method == http.MethodDelete:
		delete(m.archives, name) // with its files, as the real one does
		delete(m.archMeta, name)
		m.renameArchiveFiles(name, "")
		_, _ = w.Write([]byte(`{"success":true}`))
	}
}

func (m *mosFake) setFiles(name string, n int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.archive(name)
	m.archMeta[name]["fileCount"], m.archMeta[name]["rawSize"] = n, 13*n
}

func TestArchiveBucket(t *testing.T) {
	f := newFakeAPI(t)
	m := newMOSFake(t, f)
	cfg := func(archive string) string {
		return m.config(f) + `
resource "mtncloud_mos_bucket" "b" {
  name = "tf-acc-arch-a1"
}
resource "mtncloud_archive_bucket" "a" {
` + archive + `
}
data "mtncloud_archive_buckets" "all" {
  depends_on = [mtncloud_archive_bucket.a]
}
`
	}
	// By the bucket's name, made in the same run: its record ID is only known once it exists.
	first := cfg(`  name       = "tf-acc-arch-a1"
  mos_bucket = mtncloud_mos_bucket.b.name`)
	// By its record ID, renamed, described and public: all in place.
	changed := cfg(`  name          = "tf-acc-arch-a1-renamed"
  description   = "nightly exports"
  mos_bucket_id = mtncloud_mos_bucket.b.id
  public_url    = true`)
	force := strings.Replace(changed, "public_url    = true", "public_url    = true\n  force_destroy = true", 1)
	bumped := strings.Replace(changed, `"nightly exports"`, `"nightly exports, v2"`, 1)
	a := "mtncloud_archive_bucket.a"
	str := knownvalue.StringExact
	empty := resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()}}
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories(),
		CheckDestroy: func(*terraform.State) error {
			m.mu.Lock()
			defer m.mu.Unlock()
			if len(m.archives) != 0 {
				t.Errorf("%d archives left", len(m.archives))
			}
			return nil
		},
		Steps: []resource.TestStep{
			{
				Config: first,
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(a, tfjsonpath.New("mos_bucket_id"), str("701")),
					statecheck.ExpectKnownValue(a, tfjsonpath.New("mos_bucket"), str("tf-acc-arch-a1")),
					statecheck.ExpectKnownValue(a, tfjsonpath.New("public_url"), knownvalue.Bool(false)),
					statecheck.ExpectKnownValue(a, tfjsonpath.New("file_count"), knownvalue.Int64Exact(0)),
				},
			},
			{Config: first, ConfigPlanChecks: empty},
			{
				Config: changed,
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction(a, plancheck.ResourceActionUpdate)}},
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(a, tfjsonpath.New("name"), str("tf-acc-arch-a1-renamed")),
					statecheck.ExpectKnownValue(a, tfjsonpath.New("public_url"), knownvalue.Bool(true)),
					statecheck.ExpectKnownValue(a, tfjsonpath.New("mos_bucket"), str("tf-acc-arch-a1")),
					statecheck.ExpectKnownValue("data.mtncloud_archive_buckets.all", tfjsonpath.New("archives").AtSliceIndex(0).AtMapKey("mos_bucket_id"), str("701")),
				},
			},
			{Config: changed, ConfigPlanChecks: empty},
			{
				// A file arrives during the apply: the counts are unknown in the plan, so no inconsistent result.
				PreConfig: func() {
					m.mu.Lock()
					m.archMeta["tf-acc-arch-a1-renamed"]["uploadOnPut"] = true
					m.mu.Unlock()
				},
				Config: bumped,
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(a, tfjsonpath.New("file_count"), knownvalue.Int64Exact(1)),
				},
			},
			{
				Config: changed, ResourceName: a, ImportState: true, ImportStateId: "tf-acc-arch-a1-renamed",
				ImportStateVerify: true, ImportStateVerifyIgnore: []string{"force_destroy"},
			},
			{
				// Files in it: destroy refuses, since they would go with it.
				PreConfig: func() { m.setFiles("tf-acc-arch-a1-renamed", 2) },
				Config:    changed, Destroy: true,
				ExpectError: regexp.MustCompile(`(?s)holds 2 file.*force_destroy`),
			},
			{
				// Deleted out of band: the plan makes it again.
				PreConfig: func() {
					m.mu.Lock()
					delete(m.archives, "tf-acc-arch-a1-renamed")
					delete(m.archMeta, "tf-acc-arch-a1-renamed")
					m.mu.Unlock()
				},
				Config: changed, PlanOnly: true, ExpectNonEmptyPlan: true,
			},
			// With force_destroy the destroy at the end goes through (CheckDestroy).
			{Config: force},
		},
	})
}

// Another bucket means another archive: the old one, with its files, is replaced.
func TestArchiveBucket_OtherBucketReplaces(t *testing.T) {
	f := newFakeAPI(t)
	m := newMOSFake(t, f)
	cfg := func(bucket string) string {
		return m.config(f) + `
resource "mtncloud_mos_bucket" "one" {
  name = "tf-acc-arch-b1"
}
resource "mtncloud_mos_bucket" "two" {
  name = "tf-acc-arch-b2"
}
resource "mtncloud_archive_bucket" "a" {
  name       = "tf-acc-arch-b"
  mos_bucket = mtncloud_mos_bucket.` + bucket + `.name
}
`
	}
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories(),
		Steps: []resource.TestStep{
			{Config: cfg("one")},
			{
				Config: cfg("two"),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction("mtncloud_archive_bucket.a", plancheck.ResourceActionDestroyBeforeCreate)}},
			},
			{Config: cfg("two"), ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()}}},
		},
	})
}

func TestArchiveBucket_Checks(t *testing.T) {
	f := newFakeAPI(t)
	m := newMOSFake(t, f)
	m.records[650] = map[string]any{"id": 650, "name": "Shared", "bucketName": "shared-data", "providerType": "s3", "config": map[string]any{}}
	m.archives["taken"] = 650
	for name, c := range map[string]struct {
		body, want string
		planOnly   bool
	}{
		"both":         {`mos_bucket = "shared-data"` + "\n  mos_bucket_id = \"650\"", `(?s)Invalid Attribute Combination`, true},
		"neither":      {``, `(?s)Missing Attribute Configuration`, true},
		"unknown id":   {`mos_bucket_id = "999"`, `(?s)No such MOS bucket`, true},
		"unregistered": {`mos_bucket = "not-in-console"`, `(?s)MOS bucket isn't in the console`, false},
		"name taken":   {`mos_bucket = "shared-data"`, `(?s)named "taken" already exists.*unique across all tenants`, false},
	} {
		t.Run(name, func(t *testing.T) {
			archive := "tf-acc-arch-c"
			if name == "name taken" {
				archive = "taken"
			}
			resource.UnitTest(t, resource.TestCase{
				ProtoV6ProviderFactories: factories(),
				Steps: []resource.TestStep{{
					Config: m.config(f) + `
resource "mtncloud_archive_bucket" "a" {
  name = "` + archive + `"
  ` + c.body + `
}
`, PlanOnly: c.planOnly, ExpectError: regexp.MustCompile(c.want),
				}},
			})
		})
	}
}

func TestArchiveBucketData(t *testing.T) {
	f := newFakeAPI(t)
	m := newMOSFake(t, f)
	m.records[650] = map[string]any{"id": 650, "name": "Shared", "bucketName": "shared-data", "providerType": "s3", "config": map[string]any{}}
	m.archives["exports"] = 650
	m.setFiles("exports", 3)
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories(),
		Steps: []resource.TestStep{{
			Config: m.config(f) + `
data "mtncloud_archive_bucket" "by_name" {
  name = "exports"
}
data "mtncloud_archive_bucket" "by_id" {
  id = data.mtncloud_archive_bucket.by_name.id
}
`,
			ConfigStateChecks: []statecheck.StateCheck{
				statecheck.ExpectKnownValue("data.mtncloud_archive_bucket.by_id", tfjsonpath.New("name"), knownvalue.StringExact("exports")),
				statecheck.ExpectKnownValue("data.mtncloud_archive_bucket.by_id", tfjsonpath.New("mos_bucket_console_name"), knownvalue.StringExact("Shared")),
				statecheck.ExpectKnownValue("data.mtncloud_archive_bucket.by_name", tfjsonpath.New("file_count"), knownvalue.Int64Exact(3)),
				statecheck.ExpectKnownValue("data.mtncloud_archive_bucket.by_name", tfjsonpath.New("size_bytes"), knownvalue.Int64Exact(39)),
			},
		}},
	})
}
