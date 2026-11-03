package provider

import (
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
)

// What the plugin refuses since dc52365 (2026-11-03): a regex with a named group or a
// back-reference fails at plan time; a policy that settles in ERROR is kept and replaced on the next
// apply; a warning on an update doesn't fail it.
func TestLoadBalancerL7Policy_Errors(t *testing.T) {
	f := newFakeAPI(t)
	plugin := newLBPlugin()
	f.lb = plugin
	policy := func(value, description string) string {
		return lbBase(f) + `
resource "mtncloud_load_balancer_l7_policy" "api" {
  network_id       = 114
  load_balancer_id = mtncloud_load_balancer.web.id
  listener_id      = mtncloud_load_balancer_listener.http.id
  name             = "api"
  description      = "` + description + `"
  position         = 1
  action           = "REDIRECT_TO_POOL"
  redirect_pool_id = mtncloud_load_balancer_pool.api.id
  rules            = [{ type = "PATH", compare_type = "REGEX", value = "` + value + `" }]
}
`
	}
	res := "mtncloud_load_balancer_l7_policy.api"
	// What Python reads as characters, not back-references: \0, a three-digit octal, a digit escape
	// in [ ]; and plain and non-capturing groups, and a < on its own (live, t139).
	good := `^/(api|v2)(?:/.*)?<\\0\\101[\\1]`
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories(),
		Steps: []resource.TestStep{
			{Config: policy(`^/(?P<x>api)`, ""), PlanOnly: true, ExpectError: regexp.MustCompile(`(?s)Regex the load balancer cannot apply.*named group`)},
			{Config: policy(`^/(api)/\\1`, ""), PlanOnly: true, ExpectError: regexp.MustCompile(`(?s)Regex the load balancer cannot apply.*back-reference`)},
			{Config: policy(`^/(?P<x>api)/(?P=x)`, ""), PlanOnly: true, ExpectError: regexp.MustCompile(`named group`)},
			{Config: policy(`^/x(?<=[a-z])`, ""), PlanOnly: true, ExpectError: regexp.MustCompile(`lookbehind`)},
			{Config: policy(`^/\\p{L}`, ""), PlanOnly: true, ExpectError: regexp.MustCompile(`value\s+\^/\\p\{L\}:\s+regex\s+uses\s+\\p,\s+which\s+the\s+load\s+balancer\s+does\s+not\s+support`)},
			// Settles in ERROR: the apply fails, the policy is kept, and the next plan replaces it.
			{PreConfig: func() { plugin.l7Error = true }, Config: policy(good, ""), ExpectError: regexp.MustCompile(`(?s)is in ERROR: the load balancer\s+could not apply it`)},
			{Config: policy(good, ""), ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
				plancheck.ExpectResourceAction(res, plancheck.ResourceActionReplace),
			}}},
			{Config: policy(good, ""), ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()}}},
			// An update that answers with a warning succeeds.
			{PreConfig: func() { plugin.l7Warning = "L7 rule 'old' is in ERROR." }, Config: policy(good, "changed")},
			{Config: policy(good, "changed"), ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()}}},
		},
	})
	if plugin.runs["l7policy-create"] != 2 {
		t.Errorf("%d policy creates, want 2: the one in ERROR and its replacement, none for the refused regexes", plugin.runs["l7policy-create"])
	}
}
