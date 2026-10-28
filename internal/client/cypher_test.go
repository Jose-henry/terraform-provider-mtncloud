package client

import (
	"encoding/json"
	"net/http"
	"testing"
)

func TestDecodeCypher(t *testing.T) {
	s, err := decodeCypher(json.RawMessage(`"s3cr3t"`), 60)
	if err != nil || s.Value != "s3cr3t" || s.Values != nil || s.LeaseSeconds != 60 {
		t.Fatalf("string: %+v, %v", s, err)
	}
	s, err = decodeCypher(json.RawMessage(`{"user":"app","port":5432}`), 0)
	if err != nil || s.Values["user"] != "app" || s.Values["port"] != "5432" || s.Value != `{"user":"app","port":5432}` {
		t.Fatalf("object: %+v, %v", s, err)
	}
	if _, err = decodeCypher(json.RawMessage(`[1]`), 0); err == nil || err.Error() == "" {
		t.Fatal("a list must be refused")
	}
}

func TestCypherPath(t *testing.T) {
	if got := cypherPath("secret/a b/c"); got != "/api/cypher/secret/a%20b/c" {
		t.Fatal(got)
	}
}

// What goes on the wire: each segment escaped once (a second escape would GET, and so create,
// another key), the lease as ttl, and a delete of a missing key is no error.
func TestWriteAndDeleteCypher_Requests(t *testing.T) {
	f := newFakeAPI(t)
	var gotPath, gotTTL string
	f.route("POST", "/api/cypher/secret/a b/c", func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotTTL = r.URL.EscapedPath(), r.URL.Query().Get("ttl")
		_, _ = w.Write([]byte(`{"success":true}`))
	})
	f.route("DELETE", "/api/cypher/password/15/gone", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	c := f.client(t)
	if err := c.WriteCypher(t.Context(), "secret/a b/c", "", "8h"); err != nil {
		t.Fatal(err)
	}
	if gotPath != "/api/cypher/secret/a%20b/c" || gotTTL != "8h" {
		t.Errorf("sent path %q, ttl %q", gotPath, gotTTL)
	}
	if err := c.DeleteCypher(t.Context(), "password/15/gone"); err != nil {
		t.Errorf("deleting a missing key: %v", err)
	}
}
