package provider

import (
	"encoding/json"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

// An SMB share is created, updated and deleted by orders of the same catalog item, each with
// only the flags for what changed.
func TestSMBShare(t *testing.T) {
	f, s, _ := orderFakes(t)
	cfg := func(body string) string {
		return f.providerConfig() + "resource \"mtncloud_smb_share\" \"files\" {\n  name = \"files\"\n" + body + "}\n"
	}
	last := func(want string) func(*terraform.State) error {
		return func(*terraform.State) error {
			s.mu.Lock()
			defer s.mu.Unlock()
			got, _ := json.Marshal(s.sent[s.next-1]["config"])
			if string(got) != want {
				t.Errorf("order %d\n got %s\nwant %s", s.next-1, got, want)
			}
			return nil
		}
	}
	inPlace := resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
		plancheck.ExpectResourceAction("mtncloud_smb_share.files", plancheck.ResourceActionUpdate),
	}}
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories(),
		Steps: []resource.TestStep{
			{
				Config: cfg("  client_ips = [\"10.0.0.5\"]\n  password = \"pw-1\"\n  password_version = 1\n"),
				Check:  last(`{"clientIps":"10.0.0.5","deleteOnly":false,"quotaSize":10737418240,"smbPassword":"pw-1","updateOnly":false,"updateType":"'Replace'","volumeName":"files"}`),
			},
			{Config: cfg("  client_ips = [\"10.0.0.5\"]\n  password = \"pw-1\"\n  password_version = 1\n"),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()}}},
			{
				// A bigger quota: only updateQuota, and no password sent.
				Config: cfg("  quota_gb = 20\n  client_ips = [\"10.0.0.5\"]\n  password = \"pw-1\"\n  password_version = 1\n"), ConfigPlanChecks: inPlace,
				Check: last(`{"clientIps":"10.0.0.5","deleteOnly":false,"quotaSize":21474836480,"updateClientIps":false,"updateOnly":true,"updatePassword":false,"updateQuota":true,"updateType":"'Append'","volumeName":"files"}`),
			},
			{
				// Every client allowed.
				Config: cfg("  quota_gb = 20\n  password = \"pw-1\"\n  password_version = 1\n"), ConfigPlanChecks: inPlace,
				Check: last(`{"deleteOnly":false,"quotaSize":21474836480,"updateClientIps":true,"updateOnly":true,"updatePassword":false,"updateQuota":false,"updateType":"'Allow All'","volumeName":"files"}`),
			},
			{
				// A new password is noticed through password_version.
				Config: cfg("  quota_gb = 20\n  password = \"pw-2\"\n  password_version = 2\n"), ConfigPlanChecks: inPlace,
				Check: last(`{"deleteOnly":false,"quotaSize":21474836480,"smbPassword":"pw-2","updateClientIps":false,"updateOnly":true,"updatePassword":true,"updateQuota":false,"updateType":"'Append'","volumeName":"files"}`),
			},
		},
		CheckDestroy: func(*terraform.State) error {
			s.mu.Lock()
			defer s.mu.Unlock()
			got, _ := json.Marshal(s.sent[s.next-1]["config"])
			if want := `{"deleteOnly":true,"updateOnly":false,"updateType":"'Append'","volumeName":"files"}`; string(got) != want {
				t.Errorf("destroy order\n got %s\nwant %s", got, want)
			}
			return nil
		},
	})
}

// An NFS share updates in place with no password fields, and the next plan is empty.
func TestNFSShare(t *testing.T) {
	f, s, _ := orderFakes(t)
	cfg := func(quota string) string {
		return f.providerConfig() + "resource \"mtncloud_nfs_share\" \"data\" {\n  name = \"data\"\n  quota_gb = " + quota + "\n}\n"
	}
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories(),
		Steps: []resource.TestStep{
			{
				// The client update type always goes, quoted as the list spells it: the form's own
				// default ("Append", unquoted) is refused by the platform (live QA 2026-10-28).
				Config: cfg("10"),
				Check: func(*terraform.State) error {
					s.mu.Lock()
					defer s.mu.Unlock()
					got, _ := json.Marshal(s.sent[s.next-1]["config"])
					if want := `{"deleteOnly":false,"quotaSize":10737418240,"updateOnly":false,"updateType":"'Allow All'","volumeName":"data"}`; string(got) != want {
						t.Errorf("create order\n got %s\nwant %s", got, want)
					}
					return nil
				},
			},
			{
				Config: cfg("20"),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction("mtncloud_nfs_share.data", plancheck.ResourceActionUpdate),
				}},
				Check: func(*terraform.State) error {
					s.mu.Lock()
					defer s.mu.Unlock()
					got, _ := json.Marshal(s.sent[s.next-1]["config"])
					if want := `{"deleteOnly":false,"quotaSize":21474836480,"updateClientIps":false,"updateOnly":true,"updateQuota":true,"updateType":"'Append'","volumeName":"data"}`; string(got) != want {
						t.Errorf("update order\n got %s\nwant %s", got, want)
					}
					return nil
				},
			},
			{Config: cfg("20"), ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()}}},
			{
				// The order form can't be read: the destroy still orders the deletion.
				PreConfig: func() { f.deny("/api/catalog/types/18") },
				Config:    f.providerConfig(),
				Check: func(*terraform.State) error {
					s.mu.Lock()
					defer s.mu.Unlock()
					got, _ := json.Marshal(s.sent[s.next-1]["config"])
					if want := `{"deleteOnly":true,"updateOnly":false,"updateType":"'Append'","volumeName":"data"}`; string(got) != want {
						t.Errorf("delete order\n got %s\nwant %s", got, want)
					}
					return nil
				},
			},
		},
	})
}

// A share whose workflow fails is reported and not kept; nothing is ordered twice in one apply.
func TestShare_FailedOrder(t *testing.T) {
	f, s, _ := orderFakes(t)
	s.result = "failed"
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories(),
		Steps: []resource.TestStep{{
			Config:      f.providerConfig() + "resource \"mtncloud_nfs_share\" \"data\" {\n  name = \"data\"\n}\n",
			ExpectError: regexp.MustCompile(`Order 9801 ended with "failed"`),
		}},
		CheckDestroy: func(st *terraform.State) error {
			s.mu.Lock()
			defer s.mu.Unlock()
			if s.orders != 1 {
				t.Errorf("%d orders placed, want 1", s.orders)
			}
			if len(st.RootModule().Resources) != 0 {
				t.Errorf("a failed share was kept in state")
			}
			return nil
		},
	})
}
