package legacy

import (
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
)

// A permission set left out is no change; one that isn't JSON fails instead of being sent as
// empty (B-001: the error used to be dropped).
func TestPermissionSetJSON(t *testing.T) {
	for _, c := range []struct{ in, wantErr, wantDefault string }{
		{"", "", ""},
		{"  ", "", ""},
		{`{"default_group_permission": "read"}`, "", "read"},
		{`{"default_group_permission": `, "isn't valid JSON", ""},
	} {
		d := schema.TestResourceDataRaw(t, resourceUserRole().Schema, map[string]any{"name": "r", "permission_set": c.in})
		var data PermissionSet
		err := permissionSetJSON(d, &data)
		if (err == nil) != (c.wantErr == "") || (err != nil && !strings.Contains(err.Error(), c.wantErr)) {
			t.Errorf("%q: error %v, want %q", c.in, err, c.wantErr)
		}
		if data.DefaultGroupPermission != c.wantDefault {
			t.Errorf("%q: default group permission %q, want %q", c.in, data.DefaultGroupPermission, c.wantDefault)
		}
	}
}
