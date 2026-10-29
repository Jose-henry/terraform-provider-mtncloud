package provider

import (
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/knownvalue"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/statecheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"
)

// lbBase is a load balancer with two HTTP pools and an HTTP listener, for the pieces that hang
// off them.
func lbBase(f *fakeAPI) string {
	return f.providerConfig() + `
resource "mtncloud_load_balancer" "web" {
  network_id = 114
  name       = "web"
}
resource "mtncloud_load_balancer_pool" "web" {
  network_id       = 114
  load_balancer_id = mtncloud_load_balancer.web.id
  name             = "web"
  protocol         = "HTTP"
  algorithm        = "ROUND_ROBIN"
}
resource "mtncloud_load_balancer_pool" "api" {
  network_id       = 114
  load_balancer_id = mtncloud_load_balancer.web.id
  name             = "api"
  protocol         = "HTTP"
  algorithm        = "ROUND_ROBIN"
}
resource "mtncloud_load_balancer_listener" "http" {
  network_id       = 114
  load_balancer_id = mtncloud_load_balancer.web.id
  name             = "http"
  protocol         = "HTTP"
  port             = 80
  default_pool_id  = mtncloud_load_balancer_pool.web.id
}
`
}

func TestLoadBalancerL7Policy(t *testing.T) {
	f := newFakeAPI(t)
	plugin := newLBPlugin()
	f.lb = plugin
	policy := func(body string) string {
		return lbBase(f) + `
resource "mtncloud_load_balancer_l7_policy" "api" {
  network_id       = 114
  load_balancer_id = mtncloud_load_balancer.web.id
  listener_id      = mtncloud_load_balancer_listener.http.id
  name             = "api"
  position         = 1
` + body + `
}
data "mtncloud_load_balancer_l7_policies" "all" {
  network_id       = 114
  load_balancer_id = mtncloud_load_balancer.web.id
  listener_name    = "http"
  depends_on       = [mtncloud_load_balancer_l7_policy.api]
}
`
	}
	toPool := policy(`
  action           = "REDIRECT_TO_POOL"
  redirect_pool_id = mtncloud_load_balancer_pool.api.id
  rules = [
    { type = "PATH", compare_type = "STARTS_WITH", value = "/api" },
    { type = "HEADER", compare_type = "EQUAL_TO", key = "X-Env", value = "prod", invert = false },
  ]`)
	toURL := policy(`
  action       = "REDIRECT_TO_URL"
  redirect_url = "https://example.com/moved"
  rules        = [{ type = "HOST_NAME", compare_type = "EQUAL_TO", value = "old.example.com", invert = true }]`)
	res := "mtncloud_load_balancer_l7_policy.api"
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories(),
		Steps: []resource.TestStep{
			{Config: toPool, ConfigStateChecks: []statecheck.StateCheck{
				statecheck.ExpectKnownValue(res, tfjsonpath.New("rules"), knownvalue.SetSizeExact(2)),
				statecheck.ExpectKnownValue(res, tfjsonpath.New("redirect_http_code"), knownvalue.Null()),
				statecheck.ExpectKnownValue("data.mtncloud_load_balancer_l7_policies.all", tfjsonpath.New("l7_policies").AtSliceIndex(0).AtMapKey("rules"), knownvalue.ListSizeExact(2)),
			}},
			{Config: toPool, ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()}}},
			// invert = false in the configuration and unset on the other rule: both read back the same after import.
			{ResourceName: res, ImportState: true, ImportStateVerify: true, ImportStateVerifyIgnore: []string{"timeouts"},
				ImportStateIdFunc: func(s *terraform.State) (string, error) {
					a := s.RootModule().Resources[res].Primary.Attributes
					return fmt.Sprintf("114/%s/%s/%s", a["load_balancer_id"], a["listener_id"], a["id"]), nil
				}},
			{
				Config: toURL,
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction(res, plancheck.ResourceActionUpdate),
				}},
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(res, tfjsonpath.New("redirect_http_code"), knownvalue.Int64Exact(302)),
					statecheck.ExpectKnownValue(res, tfjsonpath.New("redirect_pool_id"), knownvalue.Null()),
					statecheck.ExpectKnownValue(res, tfjsonpath.New("rules"), knownvalue.SetSizeExact(1)),
				},
			},
			{Config: toURL, ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()}}},
			{ResourceName: res, ImportState: true, ImportStateVerify: true, ImportStateVerifyIgnore: []string{"timeouts"},
				ImportStateIdFunc: func(s *terraform.State) (string, error) {
					a := s.RootModule().Resources[res].Primary.Attributes
					return fmt.Sprintf("114/%s/%s/%s", a["load_balancer_id"], a["listener_id"], a["id"]), nil
				}},
			// Plan-time checks of what each action needs.
			{Config: policy(`action = "REDIRECT_TO_POOL"`), PlanOnly: true, ExpectError: regexp.MustCompile(`Missing redirect_pool_id`)},
			{Config: policy("action = \"REJECT\"\n  redirect_url = \"https://x.example\""), PlanOnly: true, ExpectError: regexp.MustCompile(`Unexpected redirect_url`)},
			{Config: policy(`action = "REJECT"`), PlanOnly: true, ExpectError: regexp.MustCompile(`reject everything`)},
			{Config: policy("action = \"REJECT\"\n  rules = [{ type = \"COOKIE\", compare_type = \"EQUAL_TO\", value = \"x\" }]"), PlanOnly: true,
				ExpectError: regexp.MustCompile(`Missing key`)},
			{Config: toURL, ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()}}},
		},
	})
}

// Three policies on one listener, created in one apply. The cloud takes a position only when no
// policy holds it and closes gaps, so they must be made in position order: the provider does that
// whatever order Terraform starts them in. When A goes, B and C move up to 1 and 2, which is the
// same order and no change. Forced the other way round (depends_on), the refusal says why.
func TestLoadBalancerL7Policy_Positions(t *testing.T) {
	f := newFakeAPI(t)
	plugin := newLBPlugin()
	f.lb = plugin
	pol := func(name string, pos int, extra string) string {
		return fmt.Sprintf(`
resource "mtncloud_load_balancer_l7_policy" "%[1]s" {
  network_id       = 114
  load_balancer_id = mtncloud_load_balancer.web.id
  listener_id      = mtncloud_load_balancer_listener.http.id
  name             = "%[1]s"
  position         = %[2]d
  action           = "REDIRECT_TO_POOL"
  redirect_pool_id = mtncloud_load_balancer_pool.api.id
  rules            = [{ type = "PATH", compare_type = "STARTS_WITH", value = "/%[1]s" }]
%[3]s}
`, name, pos, extra)
	}
	all := lbBase(f) + pol("a", 1, "") + pol("b", 2, "") + pol("c", 3, "")
	noA := lbBase(f) + pol("b", 2, "") + pol("c", 3, "")
	backwards := lbBase(f) + pol("a", 1, "  depends_on = [mtncloud_load_balancer_l7_policy.b]\n") + pol("b", 2, "")
	order := func(want string) func(*terraform.State) error {
		return func(*terraform.State) error {
			plugin.mu.Lock()
			defer plugin.mu.Unlock()
			for _, lb := range plugin.lbs {
				for _, l := range list(lb, "listeners") {
					var got []string
					for _, x := range list(l.(map[string]any), "l7policies_expanded") {
						got = append(got, fmt.Sprint(x.(map[string]any)["name"]))
					}
					if g := strings.Join(got, ","); g != want {
						return fmt.Errorf("policies tried in the order %s, want %s", g, want)
					}
				}
			}
			return nil
		}
	}
	pos := func(name string, want int64) statecheck.StateCheck {
		return statecheck.ExpectKnownValue("mtncloud_load_balancer_l7_policy."+name, tfjsonpath.New("position"), knownvalue.Int64Exact(want))
	}
	empty := resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()}}
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories(),
		Steps: []resource.TestStep{
			{Config: all, ConfigStateChecks: []statecheck.StateCheck{pos("a", 1), pos("b", 2), pos("c", 3)}, Check: order("a,b,c")},
			{Config: all, ConfigPlanChecks: empty},
			{Config: noA, ConfigStateChecks: []statecheck.StateCheck{pos("b", 2), pos("c", 3)}, Check: order("b,c")},
			{Config: noA, ConfigPlanChecks: empty},
			{Config: f.providerConfig()},
			{Config: backwards, ExpectError: regexp.MustCompile(`(?s)position 1 is used by another policy.*only\s+go\s+after\s+the\s+last\s+one`)},
		},
	})
}

// A refused rule rolls the new policy back, so nothing is left in state or in the cloud.
func TestLoadBalancerL7Policy_RefusedRuleLeavesNothing(t *testing.T) {
	f := newFakeAPI(t)
	plugin := newLBPlugin()
	f.lb = plugin
	cfg := lbBase(f) + `
resource "mtncloud_load_balancer_l7_policy" "bad" {
  network_id       = 114
  load_balancer_id = mtncloud_load_balancer.web.id
  listener_id      = mtncloud_load_balancer_listener.http.id
  action           = "REJECT"
  position         = 1
  rules            = [{ type = "PATH", compare_type = "EQUAL_TO", value = "refuse-me" }]
}
`
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories(),
		Steps: []resource.TestStep{
			{Config: cfg, ExpectError: regexp.MustCompile(`refuse-me is not allowed`)},
			{Config: lbBase(f), Check: func(*terraform.State) error {
				plugin.mu.Lock()
				defer plugin.mu.Unlock()
				for _, lb := range plugin.lbs {
					for _, l := range list(lb, "listeners") {
						if n := len(list(l.(map[string]any), "l7policies_expanded")); n != 0 {
							return fmt.Errorf("%d L7 policies left behind", n)
						}
					}
				}
				return nil
			}},
		},
	})
}

func TestLoadBalancerInstanceMember(t *testing.T) {
	f := newFakeAPI(t)
	plugin := newLBPlugin()
	f.lb = plugin
	cfg := func(inst string, weight int) string {
		return lbBase(f) + fmt.Sprintf(`
resource "mtncloud_load_balancer_instance_member" "app" {
  network_id       = 114
  load_balancer_id = mtncloud_load_balancer.web.id
  pool_id          = mtncloud_load_balancer_pool.web.id
  instance_id      = %s
  port             = 8080
  weight           = %d
}
data "mtncloud_load_balancer_member" "first" {
  network_id       = 114
  load_balancer_id = mtncloud_load_balancer.web.id
  pool_name        = "web"
  id               = mtncloud_load_balancer_instance_member.app.members[0].id
}
`, inst, weight)
	}
	res := "mtncloud_load_balancer_instance_member.app"
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories(),
		Steps: []resource.TestStep{
			{Config: cfg("301", 2), ConfigStateChecks: []statecheck.StateCheck{
				statecheck.ExpectKnownValue(res, tfjsonpath.New("id"), knownvalue.StringExact("301")),
				statecheck.ExpectKnownValue(res, tfjsonpath.New("members"), knownvalue.ListSizeExact(2)),
				statecheck.ExpectKnownValue(res, tfjsonpath.New("members").AtSliceIndex(0).AtMapKey("address"), knownvalue.StringExact("10.0.0.31")),
				statecheck.ExpectKnownValue("data.mtncloud_load_balancer_member.first", tfjsonpath.New("address"), knownvalue.StringExact("10.0.0.31")),
				statecheck.ExpectKnownValue("data.mtncloud_load_balancer_member.first", tfjsonpath.New("weight"), knownvalue.Int64Exact(2)),
			}},
			{Config: cfg("301", 2), ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()}}},
			{ResourceName: res, ImportState: true, ImportStateVerify: true, ImportStateVerifyIgnore: []string{"timeouts"},
				ImportStateIdFunc: func(s *terraform.State) (string, error) {
					a := s.RootModule().Resources[res].Primary.Attributes
					return fmt.Sprintf("114/%s/%s/301", a["load_balancer_id"], a["pool_id"]), nil
				}},
			{Config: cfg("301", 3), ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
				plancheck.ExpectResourceAction(res, plancheck.ResourceActionDestroyBeforeCreate),
			}}},
			// A second owner for the same instance in the same pool is refused before anything is added.
			{Config: cfg("301", 3) + `
resource "mtncloud_load_balancer_instance_member" "again" {
  network_id       = 114
  load_balancer_id = mtncloud_load_balancer.web.id
  pool_id          = mtncloud_load_balancer_pool.web.id
  instance_id      = 301
  port             = 8081
  depends_on       = [mtncloud_load_balancer_instance_member.app]
}
`, ExpectError: regexp.MustCompile(`(?s)Instance already in the pool.*terraform import`)},
			{Config: cfg("301", 3), ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()}},
				ConfigStateChecks: []statecheck.StateCheck{statecheck.ExpectKnownValue(res, tfjsonpath.New("members"), knownvalue.ListSizeExact(2))}},
			{Config: cfg("999", 3), ExpectError: regexp.MustCompile(`not one this load balancer can use`)},
			{Config: lbBase(f), Check: func(*terraform.State) error {
				plugin.mu.Lock()
				defer plugin.mu.Unlock()
				for _, lb := range plugin.lbs {
					for _, x := range list(lb, "pools") {
						if n := len(list(x.(map[string]any), "members")); n != 0 {
							return fmt.Errorf("%d members left in pool %v", n, x.(map[string]any)["name"])
						}
					}
				}
				return nil
			}},
		},
	})
}

func TestLoadBalancerFloatingIP(t *testing.T) {
	f := newFakeAPI(t)
	plugin := newLBPlugin()
	f.lb = plugin
	cfg := func(body string) string {
		return lbBase(f) + `
resource "mtncloud_load_balancer_floating_ip" "pub" {
  network_id       = 114
  load_balancer_id = mtncloud_load_balancer.web.id
  ` + body + `
}
`
	}
	res := "mtncloud_load_balancer_floating_ip.pub"
	fromPool := cfg(`floating_ip_pool = "public-network-02"`)
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories(),
		Steps: []resource.TestStep{
			{Config: cfg(`floating_ip_pool = "public-network-09"`), PlanOnly: true, ExpectError: regexp.MustCompile(`(?s)Available\s+pools: public-network-02`)},
			{Config: lbBase(f)},
			{
				// Attached in the console first: not taken over (or released on destroy) without an import.
				PreConfig: func() {
					plugin.mu.Lock()
					defer plugin.mu.Unlock()
					for _, lb := range plugin.lbs {
						lb["fip"] = map[string]any{"id": "fip-console", "floating_ip_address": "196.0.0.7", "floating_network_id": "ext-02"}
					}
				},
				Config: fromPool, ExpectError: regexp.MustCompile(`(?s)already has floating IP 196\.0\.0\.7.*terraform import`),
			},
			{
				PreConfig: func() {
					plugin.mu.Lock()
					defer plugin.mu.Unlock()
					for _, lb := range plugin.lbs {
						delete(lb, "fip")
					}
				},
				Config: fromPool, ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(res, tfjsonpath.New("address"), knownvalue.StringExact("196.0.0.10")),
					statecheck.ExpectKnownValue(res, tfjsonpath.New("release_on_destroy"), knownvalue.Bool(true)),
				}},
			{Config: fromPool, ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()}}},
			{ResourceName: res, ImportState: true, ImportStateVerify: true, ImportStateVerifyIgnore: []string{"timeouts", "floating_ip_pool", "release_on_destroy"},
				ImportStateIdFunc: func(s *terraform.State) (string, error) {
					a := s.RootModule().Resources[res].Primary.Attributes
					return fmt.Sprintf("114/%s/%s", a["load_balancer_id"], a["id"]), nil
				}},
			{
				// Detached in the console: the next plan attaches one again.
				PreConfig: func() {
					plugin.mu.Lock()
					defer plugin.mu.Unlock()
					for _, lb := range plugin.lbs {
						delete(lb, "fip")
					}
				},
				Config: fromPool, PlanOnly: true, ExpectNonEmptyPlan: true,
			},
			{Config: fromPool},
			{
				// A held address, kept in the project on destroy.
				Config: cfg("floating_ip_id = \"fip-held\"\n  release_on_destroy = false"),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction(res, plancheck.ResourceActionDestroyBeforeCreate),
				}},
				ConfigStateChecks: []statecheck.StateCheck{statecheck.ExpectKnownValue(res, tfjsonpath.New("address"), knownvalue.StringExact("196.0.0.9"))},
			},
			{
				// From the held address to a pool: a new address, never just a new name in state.
				Config: fromPool,
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction(res, plancheck.ResourceActionDestroyBeforeCreate),
				}},
				ConfigStateChecks: []statecheck.StateCheck{statecheck.ExpectKnownValue(res, tfjsonpath.New("address"), knownvalue.StringExact("196.0.0.10"))},
			},
			{Config: lbBase(f), Check: func(*terraform.State) error {
				plugin.mu.Lock()
				defer plugin.mu.Unlock()
				if got := fmt.Sprint(plugin.released); got != "[true false true]" {
					return fmt.Errorf("detaches released %s, want [true false true]", got)
				}
				return nil
			}},
		},
	})
}
