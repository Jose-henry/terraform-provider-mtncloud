package client

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func archiveFixture(t *testing.T, file string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "archives", file))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestGetArchiveBucket(t *testing.T) {
	f := newFakeAPI(t)
	f.route("GET", "/api/archives/buckets/7001", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(archiveFixture(t, "get.json"))
	})
	a, err := f.client(t).GetArchiveBucket(t.Context(), 7001)
	if err != nil {
		t.Fatal(err)
	}
	want := ArchiveBucket{ID: 7001, Name: "tf-acc-archive-docs", Description: "Release notes", StorageBucketID: 701,
		StorageBucket: "kestrel-files", Owner: "Kestrel-Dev_01", Visibility: "private", IsPublic: true, FileCount: 3, RawSize: 2048}
	if *a != want {
		t.Errorf("got %+v\nwant %+v", *a, want)
	}
}

// A 200 with no archive in it is the API's way of saying "gone": it reads as a 404.
func TestGetArchiveBucket_NullIsNotFound(t *testing.T) {
	f := newFakeAPI(t)
	f.route("GET", "/api/archives/buckets/7001", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(archiveFixture(t, "get_null.json"))
	})
	if _, err := f.client(t).GetArchiveBucket(t.Context(), 7001); !IsNotFound(err) {
		t.Errorf("err %v, want a 404", err)
	}
}

// A create answer that names no archive is an error, never an archive with ID 0 in state.
func TestCreateArchiveBucket_NoIDInAnswer(t *testing.T) {
	f := newFakeAPI(t)
	f.route("POST", "/api/archives/buckets", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(archiveFixture(t, "create_no_id.json"))
	})
	_, err := f.client(t).CreateArchiveBucket(t.Context(), ArchiveBucketInput{Name: "tf-acc-archive-docs", StorageBucketID: 701})
	if err == nil || !strings.Contains(err.Error(), "names no archive") {
		t.Errorf("err %v", err)
	}
}

// The list is read page by page until meta.total, and FindArchiveBucket looks through all of it.
func TestListArchiveBuckets_Pages(t *testing.T) {
	const total = pageSize + 3
	f := newFakeAPI(t)
	calls := 0
	f.route("GET", "/api/archives/buckets", func(w http.ResponseWriter, r *http.Request) {
		calls++
		offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
		var items []string
		for i := offset; i < min(offset+pageSize, total); i++ {
			items = append(items, fmt.Sprintf(`{"id": %d, "name": "a-%d"}`, 8000+i, i))
		}
		_, _ = fmt.Fprintf(w, `{"archiveBuckets": [%s], "meta": {"total": %d}}`, strings.Join(items, ","), total)
	})
	c := f.client(t)
	all, err := c.ListArchiveBuckets(t.Context())
	if err != nil || len(all) != total || calls != 2 {
		t.Fatalf("%d archives in %d calls, err %v", len(all), calls, err)
	}
	a, err := c.FindArchiveBucket(t.Context(), fmt.Sprintf("a-%d", total-1))
	if err != nil || a == nil || a.ID != 8000+total-1 {
		t.Errorf("found %+v, err %v", a, err)
	}
	if a, err := c.FindArchiveBucket(t.Context(), "nope"); a != nil || err != nil {
		t.Errorf("found %+v, err %v; want nothing", a, err)
	}
}
