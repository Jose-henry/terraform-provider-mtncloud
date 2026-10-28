package legacy

import (
	"strings"
	"testing"

	sdk "github.com/gomorpheus/morpheus-go-sdk"
)

func TestCypherImportKey(t *testing.T) {
	list := []sdk.Cypher{
		{ID: 10, ItemKey: "secret/app/db"},
		{ID: 11, ItemKey: "tfvars/app"},
		{ID: 12, ItemKey: "password/20/app"},
	}
	tests := []struct {
		name, mount, id, want, wantErr string
	}{
		{name: "numeric ID", mount: "secret", id: "10", want: "app/db"},
		{name: "numeric ID, tfvars", mount: "tfvars", id: "11", want: "app"},
		{name: "key with mount", mount: "secret", id: "secret/app/db", want: "app/db"},
		{name: "key without mount", mount: "secret", id: "app/db", want: "app/db"},
		{name: "ID of another mount", mount: "secret", id: "12", wantErr: "not in the secret mount"},
		{name: "unknown ID", mount: "secret", id: "99", wantErr: "no Cypher entry with ID 99"},
		{name: "mount only", mount: "secret", id: "secret/", wantErr: "has no key"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := cypherImportKey(tt.mount, tt.id, list)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("error = %v, want %q", err, tt.wantErr)
				}
				return
			}
			if err != nil || got != tt.want {
				t.Fatalf("got %q, %v; want %q", got, err, tt.want)
			}
		})
	}
}
