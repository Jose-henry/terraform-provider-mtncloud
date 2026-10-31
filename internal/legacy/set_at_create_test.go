package legacy

import (
	"context"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-sdk/v2/terraform"
)

// A change MTN Cloud can't make is refused at plan time, never a replace; a secret's value is never
// put in the error; a create and an unchanged value pass.
func TestSetAtCreate(t *testing.T) {
	ctx := context.Background()
	kp := resourceKeyPair()
	state := &terraform.InstanceState{ID: "12", Attributes: map[string]string{
		"id": "12", "name": "ops", "public_key": "ssh-ed25519 AAAA", "private_key": "s3cret-key", "passphrase": "",
	}}
	cfg := func(m map[string]interface{}) *terraform.ResourceConfig { return terraform.NewResourceConfigRaw(m) }
	base := map[string]interface{}{"name": "ops", "public_key": "ssh-ed25519 AAAA", "private_key": "s3cret-key"}
	with := func(k, v string) map[string]interface{} {
		m := map[string]interface{}{}
		for a, b := range base {
			m[a] = b
		}
		m[k] = v
		return m
	}
	for _, c := range []struct {
		name  string
		state *terraform.InstanceState
		cfg   map[string]interface{}
		want  string // "" for no error
	}{
		{"create", nil, with("name", "other"), ""},
		{"unchanged", state, base, ""},
		{"renamed", state, with("name", "ops-2"), `name can't change`},
		{"new public key", state, with("public_key", "ssh-ed25519 BBBB"), `public_key can't change`},
		{"new private key", state, with("private_key", "other-key"), `private_key can't change`},
		// Never read back, so empty after an import: the error says to remove it, not to set it back.
		{"passphrase after import", state, with("passphrase", "pp"), `If the key pair was imported, remove passphrase`},
	} {
		t.Run(c.name, func(t *testing.T) {
			d, err := kp.SimpleDiff(ctx, c.state, cfg(c.cfg), nil)
			if c.want == "" {
				if err != nil {
					t.Fatalf("error %v, want none", err)
				}
				if d != nil && d.RequiresNew() && c.state != nil {
					t.Fatal("plans a replace")
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("error %v, want %q", err, c.want)
			}
			if strings.Contains(err.Error(), "s3cret-key") || strings.Contains(err.Error(), "other-key") {
				t.Errorf("the error shows the secret: %v", err)
			}
		})
	}
}

// Cypher secrets change their value in place: the key alone is fixed.
func TestCypherSecret_ValueInPlace(t *testing.T) {
	ctx := context.Background()
	r := resourceCypherSecret()
	state := &terraform.InstanceState{ID: "5", Attributes: map[string]string{"id": "5", "key": "app/db", "value": "a", "ttl": "0"}}
	d, err := r.SimpleDiff(ctx, state, terraform.NewResourceConfigRaw(map[string]interface{}{"key": "app/db", "value": "b"}), nil)
	if err != nil || d == nil || d.RequiresNew() {
		t.Fatalf("new value: diff %v, error %v; want an in-place update", d, err)
	}
	if r.UpdateContext == nil {
		t.Fatal("no Update to send the new value")
	}
	if _, err := r.SimpleDiff(ctx, state, terraform.NewResourceConfigRaw(map[string]interface{}{"key": "app/other", "value": "a"}), nil); err == nil ||
		!strings.Contains(err.Error(), "key can't change") {
		t.Fatalf("new key: error %v, want a refusal", err)
	}
}
