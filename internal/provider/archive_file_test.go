package provider

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/knownvalue"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/statecheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"
)

type fakeArchiveFile struct {
	id      int64
	archive string
	path    string
	content []byte
	updated string
}

type fakeArchiveLink struct {
	id               int64
	secret           string
	created, expires string // expires is empty for a link that never expires
}

// archiveFileCount is how many files the archive holds. With m.mu held.
func (m *mosFake) archiveFileCount(name string) int {
	n := 0
	for _, f := range m.afiles {
		if f.archive == name {
			n++
		}
	}
	return n
}

// renameArchiveFiles follows an archive's rename, or deletes its files when to is empty.
func (m *mosFake) renameArchiveFiles(from, to string) {
	for id, f := range m.afiles {
		switch {
		case f.archive != from:
		case to == "":
			delete(m.afiles, id)
			delete(m.alinks, id)
		default:
			f.archive = to
		}
	}
}

// archiveFileRecord is a file as GET /api/archives/files/{id} answers it (live, 2026-10-29).
func (m *mosFake) archiveFileRecord(f *fakeArchiveFile) map[string]any {
	return map[string]any{
		"id": f.id, "name": path.Base(f.path), "filePath": f.path, "isDirectory": false, "status": "Active",
		"rawSize": len(f.content), "contentType": "binary/octet-stream", "downloadCount": 0,
		"dateCreated": "2026-10-29T12:04:45Z", "lastUpdated": f.updated,
		"archiveBucket": map[string]any{"id": m.archMeta[f.archive]["id"], "name": f.archive, "isPublic": false},
	}
}

func (m *mosFake) setArchiveFile(id int64, content string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if f := m.afiles[id]; f != nil {
		f.content = []byte(content)
	}
}

func (m *mosFake) fileAt(p string) *fakeArchiveFile {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, f := range m.afiles {
		if f.path == p {
			return f
		}
	}
	return nil
}

// serveArchiveFiles is the archive files and links API, with m.mu held.
func (m *mosFake) serveArchiveFiles(w http.ResponseWriter, r *http.Request) {
	p := r.URL.Path
	if rest, ok := strings.CutPrefix(p, "/api/archives/files/"); ok {
		parts := strings.Split(rest, "/")
		id, _ := strconv.ParseInt(parts[0], 10, 64)
		f := m.afiles[id]
		if f == nil {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"success":false,"msg":"Archive File not found"}`))
			return
		}
		switch {
		case len(parts) == 1 && r.Method == http.MethodGet:
			_ = json.NewEncoder(w).Encode(map[string]any{"archiveFile": m.archiveFileRecord(f), "isOwner": true})
		case len(parts) == 1 && r.Method == http.MethodDelete:
			delete(m.afiles, id)
			delete(m.alinks, id)
			_, _ = w.Write([]byte(`{"success":true}`))
		case len(parts) == 2 && r.Method == http.MethodGet:
			rows := []any{}
			for _, l := range m.alinks[id] {
				var exp any
				if l.expires != "" {
					exp = l.expires
				}
				rows = append(rows, map[string]any{"id": l.id, "secretAccessKey": l.secret, "dateCreated": l.created, "expirationDate": exp,
					"downloadCount": 0, "archiveFile": map[string]any{"id": id}})
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"archiveFileLinks": rows, "archiveFileLinkCount": len(rows)})
		case len(parts) == 2 && r.Method == http.MethodPost:
			m.next++
			now := time.Date(2026, 10, 29, 12, 5, 20, 0, time.UTC)
			l := &fakeArchiveLink{id: m.next, secret: fmt.Sprintf("5ec7e7%010d", m.next), created: now.Format(time.RFC3339)}
			if s := r.URL.Query().Get("expireSeconds"); s != "" {
				n, _ := strconv.Atoi(s)
				l.expires = now.Add(time.Duration(n) * time.Second).Format(time.RFC3339)
			}
			m.alinks[id] = append(m.alinks[id], l)
			_, _ = fmt.Fprintf(w, `{"success":true,"secretAccessKey":%q}`, l.secret) // no link ID, as live
		case len(parts) == 3 && r.Method == http.MethodDelete:
			lid, _ := strconv.ParseInt(parts[2], 10, 64)
			kept := m.alinks[id][:0]
			for _, l := range m.alinks[id] {
				if l.id != lid {
					kept = append(kept, l)
				}
			}
			m.alinks[id] = kept
			_, _ = w.Write([]byte(`{"success":true}`))
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
		return
	}
	// /api/archives/buckets/{archive}/files/{folder...}
	rest := strings.TrimPrefix(p, "/api/archives/buckets/")
	archive, folder, _ := strings.Cut(rest, "/files")
	folder = strings.Trim(folder, "/")
	if m.archives[archive] == 0 {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	if r.Method == http.MethodPost {
		name := r.URL.Query().Get("filename")
		full := strings.TrimPrefix(folder+"/"+name, "/")
		body, _ := io.ReadAll(r.Body)
		var f *fakeArchiveFile
		for _, x := range m.afiles {
			if x.archive == archive && x.path == full {
				f = x // overwritten in place: the ID stays (live)
			}
		}
		if f == nil {
			m.next++
			f = &fakeArchiveFile{id: m.next, archive: archive, path: full}
			m.afiles[f.id] = f
		}
		f.content = body
		f.updated = time.Date(2026, 10, 29, 12, 0, int(m.next%60), 0, time.UTC).Format(time.RFC3339)
		_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "archiveFile": map[string]any{
			"id": f.id, "name": name, "filePath": full, "isDirectory": false,
			"archiveBucket": map[string]any{"id": m.archMeta[archive]["id"], "name": archive}}})
		return
	}
	recursive := r.URL.Query().Get("fullTree") == "true"
	rows, dirs := []any{}, map[string]bool{}
	for _, f := range m.afiles {
		if f.archive != archive || (folder != "" && !strings.HasPrefix(f.path, folder+"/")) {
			continue
		}
		rel := strings.TrimPrefix(strings.TrimPrefix(f.path, folder), "/")
		if dir, _, deeper := strings.Cut(rel, "/"); deeper && !recursive {
			dirs[strings.TrimPrefix(folder+"/"+dir, "/")] = true
			continue
		}
		rows = append(rows, m.archiveFileRecord(f))
	}
	for d := range dirs {
		rows = append(rows, map[string]any{"id": 9000 + len(d), "name": path.Base(d), "filePath": d, "isDirectory": true})
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"archiveFiles": rows, "archiveFileCount": len(rows)})
}

func sha256Of(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func TestArchiveFile(t *testing.T) {
	f := newFakeAPI(t)
	m := newMOSFake(t, f)
	src := filepath.Join(t.TempDir(), "two.txt")
	if err := os.WriteFile(src, []byte("from a file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := func(content string) string {
		return m.config(f) + `
resource "mtncloud_mos_bucket" "b" {
  name = "tf-acc-files-1"
}
resource "mtncloud_archive_bucket" "a" {
  name          = "tf-acc-files-1"
  mos_bucket_id = mtncloud_mos_bucket.b.id
  force_destroy = true
}
resource "mtncloud_archive_file" "one" {
  archive = mtncloud_archive_bucket.a.name
  path    = "docs/note.txt"
  content = "` + content + `"
}
resource "mtncloud_archive_file" "two" {
  archive_id = mtncloud_archive_bucket.a.id
  path       = "docs/deep/two.txt"
  source     = "` + filepath.ToSlash(src) + `"
}
resource "mtncloud_archive_file_link" "never" {
  file_id        = mtncloud_archive_file.one.id
  expire_seconds = 0
}
resource "mtncloud_archive_file_link" "hour" {
  file_id        = mtncloud_archive_file.two.id
  expire_seconds = 3600
}
data "mtncloud_archive_files" "docs" {
  archive_id = mtncloud_archive_bucket.a.id
  folder     = "docs"
  depends_on = [mtncloud_archive_file.one, mtncloud_archive_file.two]
}
`
	}
	one, two, never, hour := "mtncloud_archive_file.one", "mtncloud_archive_file.two", "mtncloud_archive_file_link.never", "mtncloud_archive_file_link.hour"
	str := knownvalue.StringExact
	empty := resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()}}
	update := func(addr string) resource.ConfigPlanChecks {
		return resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
			plancheck.ExpectResourceAction(addr, plancheck.ResourceActionUpdate),
			plancheck.ExpectResourceAction(never, plancheck.ResourceActionNoop)}}
	}
	var linkID string
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories(),
		CheckDestroy: func(*terraform.State) error {
			m.mu.Lock()
			defer m.mu.Unlock()
			if len(m.afiles) != 0 || len(m.archives) != 0 {
				t.Errorf("%d files and %d archives left", len(m.afiles), len(m.archives))
			}
			return nil
		},
		Steps: []resource.TestStep{
			{
				Config: cfg("hello one"),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(one, tfjsonpath.New("content_sha256"), str(sha256Of("hello one"))),
					statecheck.ExpectKnownValue(one, tfjsonpath.New("size_bytes"), knownvalue.Int64Exact(9)),
					statecheck.ExpectKnownValue(one, tfjsonpath.New("archive_id"), knownvalue.NotNull()),
					statecheck.ExpectKnownValue(two, tfjsonpath.New("archive"), str("tf-acc-files-1")),
					statecheck.ExpectKnownValue(two, tfjsonpath.New("content_type"), str("binary/octet-stream")),
					statecheck.ExpectKnownValue(never, tfjsonpath.New("expires_at"), str("")),
					statecheck.ExpectKnownValue(never, tfjsonpath.New("download_url"),
						knownvalue.StringRegexp(regexp.MustCompile(`[^i]/public-archives/link\?s=5ec7e7[0-9]+$`))),
					statecheck.ExpectKnownValue(hour, tfjsonpath.New("expires_at"), str("2026-10-29T13:05:20Z")),
					statecheck.ExpectKnownValue("data.mtncloud_archive_files.docs", tfjsonpath.New("files"), knownvalue.ListExact([]knownvalue.Check{
						knownvalue.ObjectPartial(map[string]knownvalue.Check{"path": str("docs/deep"), "is_directory": knownvalue.Bool(true)}),
						knownvalue.ObjectPartial(map[string]knownvalue.Check{"path": str("docs/note.txt"), "size_bytes": knownvalue.Int64Exact(9)}),
					})),
				},
			},
			{Config: cfg("hello one"), ConfigPlanChecks: empty},
			// New content: uploaded again in place, and the file keeps its ID, so its link stays.
			{Config: cfg("hello again"), ConfigPlanChecks: update(one),
				ConfigStateChecks: []statecheck.StateCheck{statecheck.ExpectKnownValue(one, tfjsonpath.New("size_bytes"), knownvalue.Int64Exact(11))}},
			{Config: cfg("hello again"), ConfigPlanChecks: empty},
			{
				// The source file changed on disk.
				PreConfig: func() { _ = os.WriteFile(src, []byte("from a file, edited\n"), 0o600) },
				Config:    cfg("hello again"), ConfigPlanChecks: update(two),
			},
			{Config: cfg("hello again"), ConfigPlanChecks: empty},
			{
				// Overwritten in the console with other content: the size changed, so it is uploaded again.
				PreConfig: func() { m.setArchiveFile(m.fileAt("docs/note.txt").id, "changed in the console") },
				Config:    cfg("hello again"), ConfigPlanChecks: update(one),
			},
			{Config: cfg("hello again"), ConfigPlanChecks: empty},
			{
				Config: cfg("hello again"), ResourceName: one, ImportState: true, ImportStateId: "tf-acc-files-1/docs/note.txt",
				ImportStateVerify: true, ImportStateVerifyIgnore: []string{"content", "content_sha256"},
			},
			{
				Config: cfg("hello again"), ResourceName: hour, ImportState: true, ImportStateVerify: true,
				ImportStateIdFunc: func(s *terraform.State) (string, error) {
					linkID = s.RootModule().Resources[hour].Primary.ID
					return linkID, nil
				},
			},
			{
				// Expired: the next plan makes a new link.
				PreConfig: func() {
					m.mu.Lock()
					defer m.mu.Unlock()
					for _, ls := range m.alinks {
						for _, l := range ls {
							if l.expires != "" {
								l.expires = "2020-01-01T00:00:00Z"
							}
						}
					}
				},
				Config: cfg("hello again"),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction(hour, plancheck.ResourceActionCreate)}},
			},
			{
				// Deleted in the console: made again, and its link with it.
				PreConfig: func() {
					m.mu.Lock()
					defer m.mu.Unlock()
					for id, x := range m.afiles {
						if x.path == "docs/deep/two.txt" {
							delete(m.afiles, id)
							delete(m.alinks, id)
						}
					}
				},
				Config: cfg("hello again"),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction(two, plancheck.ResourceActionCreate),
					plancheck.ExpectResourceAction(hour, plancheck.ResourceActionCreate)}}, // its links went with it
			},
			{Config: cfg("hello again"), ConfigPlanChecks: empty},
		},
	})
	if linkID == "" || !strings.Contains(linkID, "/") {
		t.Errorf("link ID %q, want <file_id>/<link_id>", linkID)
	}
}

func TestArchiveFile_Checks(t *testing.T) {
	f := newFakeAPI(t)
	m := newMOSFake(t, f)
	m.mu.Lock()
	m.records[701] = map[string]any{"id": 701, "name": "shared", "bucketName": "shared-data"}
	m.archives["exports"] = 701
	m.archive("exports")
	m.next++
	m.afiles[m.next] = &fakeArchiveFile{id: m.next, archive: "exports", path: "taken.txt", content: []byte("x")}
	m.mu.Unlock()
	cases := map[string]struct{ body, want string }{
		"both":       {"archive = \"exports\"\n  archive_id = \"1\"\n  path = \"a.txt\"\n  content = \"x\"", `(?s)Invalid Attribute Combination`},
		"no content": {"archive = \"exports\"\n  path = \"a.txt\"", `(?s)Invalid Attribute Combination|Missing Attribute`},
		"bad path":   {"archive = \"exports\"\n  path = \"/a.txt\"\n  content = \"x\"", `(?s)no .*at\s+either end`},
		"dot part":   {"archive = \"exports\"\n  path = \"docs/../x.txt\"\n  content = \"x\"", `(?s)has a \. or \.\. part`},
		"empty part": {"archive = \"exports\"\n  path = \"a//b.txt\"\n  content = \"x\"", `(?s)no empty part`},
		"no archive": {"archive_id = \"999\"\n  path = \"a.txt\"\n  content = \"x\"", `(?s)No such archive`},
		"file there": {"archive = \"exports\"\n  path = \"taken.txt\"\n  content = \"x\"", `(?s)already has a file at taken.txt.*import`},
		"bad base64": {"archive = \"exports\"\n  path = \"a.bin\"\n  content_base64 = \"%%%\"", `(?s)isn't valid base64`},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			resource.UnitTest(t, resource.TestCase{
				ProtoV6ProviderFactories: factories(),
				Steps: []resource.TestStep{{
					Config:      m.config(f) + "resource \"mtncloud_archive_file\" \"x\" {\n  " + c.body + "\n}\n",
					ExpectError: regexp.MustCompile(c.want),
				}},
			})
		})
	}
	// The link's import ID is checked.
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories(),
		Steps: []resource.TestStep{{
			Config:       m.config(f) + "resource \"mtncloud_archive_file_link\" \"x\" {\n  file_id = \"1\"\n  expire_seconds = 0\n}\n",
			ResourceName: "mtncloud_archive_file_link.x", ImportState: true, ImportStateId: "1626",
			ExpectError: regexp.MustCompile(`<file_id>/<link_id>`),
		}},
	})
}
