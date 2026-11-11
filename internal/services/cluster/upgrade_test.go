package cluster

import (
	"encoding/json"
	"testing"
)

func TestRenameDiskDatastore(t *testing.T) {
	for _, tc := range []struct{ name, in, want string }{
		{"renamed", `{"master":{"plan":"p","root_datastore":"A"},"worker":{"root_datastore":"B"}}`,
			`{"master":{"disk_datastore":"A","plan":"p"},"worker":{"disk_datastore":"B"}}`},
		{"unset stays null", `{"host":{"plan":"p","root_datastore":null}}`, `{"host":{"disk_datastore":null,"plan":"p"}}`},
		{"no worker block", `{"master":{"root_datastore":"A"},"worker":null}`, `{"master":{"disk_datastore":"A"},"worker":null}`},
		{"already renamed", `{"host":{"disk_datastore":"A"}}`, `{"host":{"disk_datastore":"A"}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := renameDiskDatastore([]byte(tc.in))
			if err != nil {
				t.Fatal(err)
			}
			var g, w any
			_ = json.Unmarshal(got, &g)
			_ = json.Unmarshal([]byte(tc.want), &w)
			gb, _ := json.Marshal(g)
			wb, _ := json.Marshal(w)
			if string(gb) != string(wb) {
				t.Errorf("got %s, want %s", gb, wb)
			}
		})
	}
	if _, err := renameDiskDatastore([]byte("{")); err == nil {
		t.Error("bad JSON: want an error")
	}
}
