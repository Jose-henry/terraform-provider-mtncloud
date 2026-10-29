package client

import (
	"io"
	"net/http"
	"testing"
)

func TestArchivePath_EscapesEachPart(t *testing.T) {
	for in, want := range map[string]string{
		"docs":            "/api/archives/buckets/my%20files/files/docs",
		"/docs/2026 q4/":  "/api/archives/buckets/my%20files/files/docs/2026%20q4",
		".":               "/api/archives/buckets/my%20files/files/",
		"":                "/api/archives/buckets/my%20files/files/",
		"a/b%c/d?.txt":    "/api/archives/buckets/my%20files/files/a/b%25c/d%3F.txt",
		"reports/x#y.pdf": "/api/archives/buckets/my%20files/files/reports/x%23y.pdf",
	} {
		if got := archivePath("my files", in); got != want {
			t.Errorf("archivePath(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestGetArchiveBucket_EmptyAnswerIsNotFound(t *testing.T) {
	f := newFakeAPI(t)
	f.route("GET", "/api/archives/buckets/7", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{}`)) })
	f.route("GET", "/api/archives/files/9", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{}`)) })
	c := f.client(t)
	if _, err := c.GetArchiveBucket(t.Context(), 7); !IsNotFound(err) {
		t.Errorf("archive: err %v, want not found", err)
	}
	if _, err := c.GetArchiveFile(t.Context(), 9); !IsNotFound(err) {
		t.Errorf("file: err %v, want not found", err)
	}
}

func TestCreateArchiveBucket_AnswerWithoutIDIsAnError(t *testing.T) {
	f := newFakeAPI(t)
	f.route("POST", "/api/archives/buckets", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"success":true,"archiveBucket":{}}`))
	})
	if _, err := f.client(t).CreateArchiveBucket(t.Context(), ArchiveBucketInput{Name: "x", StorageBucketID: 1}); err == nil {
		t.Error("no error for an answer without an archive")
	}
}

// The upload sends the raw content, the folder in the path and the name in filename.
func TestUploadArchiveFile_SendsTheRawContent(t *testing.T) {
	f := newFakeAPI(t)
	var body, ctype, name string
	f.route("POST", "/api/archives/buckets/exports/files/docs/2026", func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		body, ctype, name = string(b), r.Header.Get("Content-Type"), r.URL.Query().Get("filename")
		_, _ = w.Write([]byte(`{"success":true,"archiveFile":{"id":1626,"name":"note.txt","filePath":"docs/2026/note.txt"}}`))
	})
	a, err := f.client(t).UploadArchiveFile(t.Context(), "exports", "/docs/2026/note.txt", []byte("hello\x00bin"))
	if err != nil {
		t.Fatal(err)
	}
	if a.ID != 1626 || body != "hello\x00bin" || ctype != "application/octet-stream" || name != "note.txt" {
		t.Errorf("id %d, body %q, type %q, filename %q", a.ID, body, ctype, name)
	}
}

// An upload answer without the file: it is found by its path.
func TestUploadArchiveFile_FindsTheFileWhenTheAnswerHasNone(t *testing.T) {
	f := newFakeAPI(t)
	f.route("POST", "/api/archives/buckets/exports/files/", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"success":true}`))
	})
	f.route("GET", "/api/archives/buckets/exports/files/", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"archiveFiles":[{"id":5,"filePath":"top.txt","isDirectory":true},{"id":6,"filePath":"/top.txt"}],"meta":{"total":2}}`))
	})
	a, err := f.client(t).UploadArchiveFile(t.Context(), "exports", "top.txt", nil)
	if err != nil || a.ID != 6 || a.FilePath != "top.txt" {
		t.Fatalf("got %+v, %v; want file 6 at top.txt", a, err)
	}
}

// The create answer has only the secret (live): the link is found among the file's links by it.
func TestCreateArchiveFileLink_FindsTheLinkBySecret(t *testing.T) {
	f := newFakeAPI(t)
	var expire string
	f.route("POST", "/api/archives/files/1626/links", func(w http.ResponseWriter, r *http.Request) {
		expire = r.URL.Query().Get("expireSeconds")
		_, _ = w.Write([]byte(`{"success":true,"secretAccessKey":"5ec7e7b"}`))
	})
	f.route("GET", "/api/archives/files/1626/links", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"archiveFileLinks":[{"id":7,"secretAccessKey":"5ec7e7a","expirationDate":null},` +
			`{"id":8,"secretAccessKey":"5ec7e7b","dateCreated":"2026-10-29T12:05:20Z","expirationDate":"2026-10-29T12:06:20Z"}],"meta":{"total":2}}`))
	})
	c := f.client(t)
	l, err := c.CreateArchiveFileLink(t.Context(), 1626, 60)
	if err != nil {
		t.Fatal(err)
	}
	if l.ID != 8 || l.FileID != 1626 || l.ExpirationDate != "2026-10-29T12:06:20Z" || expire != "60" {
		t.Errorf("got %+v, expireSeconds %q", l, expire)
	}
	// 0 means never: the parameter is left out, as the CLI does (live).
	if _, err := c.CreateArchiveFileLink(t.Context(), 1626, 0); err != nil || expire != "" {
		t.Errorf("never-expiring link: err %v, expireSeconds %q", err, expire)
	}
	if got := c.ArchiveLinkURL("5ec7e7b"); got != f.srv.URL+"/public-archives/link?s=5ec7e7b" {
		t.Errorf("link URL %q", got)
	}
}

// A link whose answer was lost is the one link that wasn't there before: never an untracked public link.
func TestCreateArchiveFileLink_LostAnswerFindsTheNewLink(t *testing.T) {
	f := newFakeAPI(t)
	made := false
	f.route("POST", "/api/archives/files/1626/links", func(w http.ResponseWriter, _ *http.Request) {
		made = true
		w.WriteHeader(http.StatusBadGateway) // the gateway lost the answer; the link exists
	})
	f.route("GET", "/api/archives/files/1626/links", func(w http.ResponseWriter, _ *http.Request) {
		rows := `{"id":7,"secretAccessKey":"5ec7e7a"}`
		if made {
			rows += `,{"id":9,"secretAccessKey":"5ec7e7c","expirationDate":null}`
		}
		_, _ = w.Write([]byte(`{"archiveFileLinks":[` + rows + `],"meta":{"total":` + map[bool]string{false: "1", true: "2"}[made] + `}}`))
	})
	l, err := f.client(t, fastPoll).CreateArchiveFileLink(t.Context(), 1626, 0)
	if err != nil || l.ID != 9 || l.Secret != "5ec7e7c" {
		t.Fatalf("got %+v, %v; want the new link 9", l, err)
	}
}
