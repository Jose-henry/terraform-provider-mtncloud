package compute

import (
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/types"
)

// A time in the wrong format gets only the format error, not also a misleading "off is before on"
// (tester's fix 17: on = "25:00" said "off (22:00) is before on (25:00)" too).
func TestValidDay_BadTimeIsOneError(t *testing.T) {
	for _, tc := range []struct{ on, off, want string }{
		{"25:00", "22:00", ""},
		{"07:00", "7:00", ""},
		{"19:00", "07:00", "monday: off (07:00) is before on (19:00). MTN Cloud refuses it."},
	} {
		got := validDay("monday", dayModel{On: types.StringValue(tc.on), Off: types.StringValue(tc.off)})
		if got != tc.want {
			t.Errorf("on %s off %s: %q, want %q", tc.on, tc.off, got, tc.want)
		}
	}
}
