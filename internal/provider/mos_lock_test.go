package provider

import (
	"fmt"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/knownvalue"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/statecheck"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"
)

func TestMOSObjectLock(t *testing.T) {
	f := newFakeAPI(t)
	m := newMOSFake(t, f)
	cfg := func(until string, bypass bool) string {
		return m.config(f) + fmt.Sprintf(`
resource "mtncloud_mos_bucket" "vault" {
  name                = "tf-acc-mos-vault"
  object_lock_enabled = true
  force_destroy       = true
}
resource "mtncloud_mos_bucket_object_lock" "vault" {
  bucket = mtncloud_mos_bucket.vault.name
  days   = 30
}
resource "mtncloud_mos_object" "record" {
  bucket                       = mtncloud_mos_bucket.vault.name
  key                          = "records/2026.csv"
  content                      = "a,b"
  retention_mode               = "GOVERNANCE"
  retain_until                 = %q
  bypass_governance_on_destroy = %t
  depends_on                   = [mtncloud_mos_bucket_object_lock.vault]
}
data "mtncloud_mos_bucket" "vault" {
  name       = mtncloud_mos_bucket.vault.name
  depends_on = [mtncloud_mos_bucket_object_lock.vault]
}
`, until, bypass)
	}
	empty := resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()}}
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories(),
		CheckDestroy:             m.checkDestroyed("tf-acc-mos-vault"),
		Steps: []resource.TestStep{
			{
				// Written with an offset: MOS answers in UTC, and the configuration's spelling is kept.
				Config: cfg("2030-01-01T01:00:00+01:00", false),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue("mtncloud_mos_bucket.vault", tfjsonpath.New("object_lock_enabled"), knownvalue.Bool(true)),
					statecheck.ExpectKnownValue("mtncloud_mos_bucket_object_lock.vault", tfjsonpath.New("mode"), knownvalue.StringExact("GOVERNANCE")),
					statecheck.ExpectKnownValue("data.mtncloud_mos_bucket.vault", tfjsonpath.New("default_retention_days"), knownvalue.Int64Exact(30)),
					statecheck.ExpectKnownValue("data.mtncloud_mos_bucket.vault", tfjsonpath.New("object_lock_enabled"), knownvalue.Bool(true)),
				},
			},
			{Config: cfg("2030-01-01T01:00:00+01:00", false), ConfigPlanChecks: empty},
			{
				// Later is always allowed, in place.
				Config: cfg("2031-01-01T00:00:00Z", false),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction("mtncloud_mos_object.record", plancheck.ResourceActionUpdate)}},
			},
			{Config: cfg("2031-01-01T00:00:00Z", false), ConfigPlanChecks: empty},
			{Config: cfg("2029-01-01T00:00:00Z", false), PlanOnly: true, ExpectError: regexp.MustCompile(`(?s)Retention can't be shortened.*bypass_governance_on_destroy = true`)},
			{
				// A destroy while retention holds it says how to get past it.
				Config: cfg("2031-01-01T00:00:00Z", false), Destroy: true,
				ExpectError: regexp.MustCompile(`(?s)under GOVERNANCE retention until.*bypass_governance_on_destroy = true`),
			},
			// With bypass: shortened in place, and the final destroy deletes it (CheckDestroy).
			{Config: cfg("2029-01-01T00:00:00Z", true)},
			{Config: cfg("2029-01-01T00:00:00Z", true), ConfigPlanChecks: empty},
		},
	})
}
