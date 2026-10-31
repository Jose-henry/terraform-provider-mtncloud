package legacy

import (
	"encoding/json"
	"strings"
	"testing"

	sdk "github.com/gomorpheus/morpheus-go-sdk"
)

func TestFindOptionSourceOption(t *testing.T) {
	opts := []sdk.OptionSourceOption{
		{Name: "Alpha", Value: float64(3)},
		{Name: "beta", Value: "7"},
		{Name: "gamma", Value: json.Number("9")},
		{Name: "heading", Value: ""},
	}
	tests := []struct {
		name    string
		data    *[]sdk.OptionSourceOption
		byName  string
		byID    int
		wantID  int64
		wantErr string
	}{
		{name: "by name, any case", data: &opts, byName: "alpha", wantID: 3},
		{name: "by name, string value", data: &opts, byName: "BETA", wantID: 7},
		{name: "by id, float value", data: &opts, byID: 3, wantID: 3},
		{name: "by id, string value", data: &opts, byID: 7, wantID: 7},
		{name: "by id, json number", data: &opts, byID: 9, wantID: 9},
		{name: "unknown name lists the choices", data: &opts, byName: "delta", wantErr: `no thing named "delta". Available: Alpha, beta, gamma`},
		{name: "unknown id", data: &opts, byID: 42, wantErr: "no thing with id 42"},
		{name: "empty list", data: &[]sdk.OptionSourceOption{}, byName: "alpha", wantErr: "none is available"},
		{name: "nil list", data: nil, byName: "alpha", wantErr: "none is available"},
		{name: "neither name nor id", data: &opts, wantErr: "cannot be read without name or id"},
		// A value that isn't a number is unreadable, not missing (B-016).
		{name: "named option with a text value", data: &opts, byName: "heading", wantErr: `thing "heading" exists, but its value () isn't a number`},
		{name: "only unreadable options", data: &[]sdk.OptionSourceOption{{Name: "x", Value: "abc"}}, byName: "alpha",
			wantErr: "have values that aren't numbers"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, id, err := findOptionSourceOption(tt.data, "thing", tt.byName, tt.byID)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("error = %v, want it to contain %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if id != tt.wantID {
				t.Fatalf("id = %d, want %d", id, tt.wantID)
			}
		})
	}
}
