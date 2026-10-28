package network

import (
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/mtn/terraform-provider-mtncloud/internal/client"
)

// After the cloud's sync a group made without a description reads back with its name as the
// description: that must not show as a change to clear it.
func TestRefresh_SyncedDescription(t *testing.T) {
	tests := []struct {
		name  string
		state types.String
		api   string
		want  types.String
	}{
		{name: "unset, synced to the name", state: types.StringNull(), api: "web", want: types.StringNull()},
		{name: "unset, never synced", state: types.StringNull(), api: "", want: types.StringNull()},
		{name: "unset, set in the console", state: types.StringNull(), api: "front end", want: types.StringValue("front end")},
		{name: "set to the name", state: types.StringValue("web"), api: "web", want: types.StringValue("web")},
		{name: "set, changed outside", state: types.StringValue("old"), api: "new", want: types.StringValue("new")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := securityGroupModel{Description: tt.state, Project: types.StringValue("admin")}
			g := &client.SecurityGroup{ID: 1, Name: "web", Description: tt.api}
			if d := refresh(t.Context(), &m, g, "Acme", false, nil); d.HasError() {
				t.Fatal(d)
			}
			if !m.Description.Equal(tt.want) {
				t.Errorf("description = %v, want %v", m.Description, tt.want)
			}
		})
	}
}
