package client

import (
	"encoding/json"
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
