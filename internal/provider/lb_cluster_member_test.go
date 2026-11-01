package provider

import (
	"fmt"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/knownvalue"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/statecheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"
)

// A cluster role's nodes join a pool as one group (the plugin's clusterGroup, B-023): read back by
// the group's tag, weight and backup changed in place on each member, removed together.
func TestLoadBalancerClusterMember(t *testing.T) {
	f := newFakeAPI(t)
	plugin := newLBPlugin()
	f.lb = plugin
	cfg := func(role string, weight int, backup bool) string {
		return lbBase(f) + fmt.Sprintf(`
resource "mtncloud_load_balancer_cluster_member" "k8s" {
  network_id       = 114
  load_balancer_id = mtncloud_load_balancer.web.id
  pool_id          = mtncloud_load_balancer_pool.web.id
  cluster_id       = 143
  role             = %q
  port             = 30080
  weight           = %d
  backup           = %t
}
`, role, weight, backup)
	}
	res := "mtncloud_load_balancer_cluster_member.k8s"
	empty := resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()}}
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories(),
		Steps: []resource.TestStep{
			{Config: cfg("Kube Worker", 1, false), ExpectError: regexp.MustCompile(`must be a node type such as kube-worker`)},
			{Config: cfg("kube-worker", 2, false), ConfigStateChecks: []statecheck.StateCheck{
				statecheck.ExpectKnownValue(res, tfjsonpath.New("id"), knownvalue.StringExact("143:kube-worker")),
				statecheck.ExpectKnownValue(res, tfjsonpath.New("members"), knownvalue.ListSizeExact(3)),
				statecheck.ExpectKnownValue(res, tfjsonpath.New("members").AtSliceIndex(0).AtMapKey("address"), knownvalue.StringExact("10.0.0.41")),
				statecheck.ExpectKnownValue(res, tfjsonpath.New("members").AtSliceIndex(2).AtMapKey("address"), knownvalue.StringExact("10.0.0.43")),
			}},
			{Config: cfg("kube-worker", 2, false), ConfigPlanChecks: empty},
			{ResourceName: res, ImportState: true, ImportStateVerify: true, ImportStateVerifyIgnore: []string{"timeouts"},
				ImportStateIdFunc: func(s *terraform.State) (string, error) {
					a := s.RootModule().Resources[res].Primary.Attributes
					return fmt.Sprintf("114/%s/%s/143:kube-worker", a["load_balancer_id"], a["pool_id"]), nil
				}},
			{ResourceName: res, ImportState: true, ImportStateId: "114/lb/pool/143", ExpectError: regexp.MustCompile(`must be <cluster_id>:<role>`)},
			// Weight and backup change in place on each member: never removed and added again.
			{Config: cfg("kube-worker", 5, true), ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
				plancheck.ExpectResourceAction(res, plancheck.ResourceActionUpdate),
			}}, ConfigStateChecks: []statecheck.StateCheck{
				statecheck.ExpectKnownValue(res, tfjsonpath.New("weight"), knownvalue.Int64Exact(5)),
				statecheck.ExpectKnownValue(res, tfjsonpath.New("backup"), knownvalue.Bool(true)),
			}, Check: func(*terraform.State) error {
				plugin.mu.Lock()
				defer plugin.mu.Unlock()
				if n := plugin.runs["member-update"]; n != 3 {
					return fmt.Errorf("%d member updates, want 3 (one per node)", n)
				}
				if n := plugin.runs["member-add"]; n != 1 {
					return fmt.Errorf("%d member adds, want only the first", n)
				}
				return nil
			}},
			{Config: cfg("kube-worker", 5, true), ConfigPlanChecks: empty},
			// The port can't change on a member: refused at plan time, nothing replaced.
			{Config: replacePort(cfg("kube-worker", 5, true)), ExpectError: regexp.MustCompile(`port can't change`)},
			// A second owner for the same group in the same pool is refused before anything is added.
			{Config: cfg("kube-worker", 5, true) + `
resource "mtncloud_load_balancer_cluster_member" "again" {
  network_id       = 114
  load_balancer_id = mtncloud_load_balancer.web.id
  pool_id          = mtncloud_load_balancer_pool.web.id
  cluster_id       = 143
  role             = "kube-worker"
  port             = 30081
  depends_on       = [mtncloud_load_balancer_cluster_member.k8s]
}
`, ExpectError: regexp.MustCompile(`(?s)Cluster role already in the pool.*terraform import`)},
			{Config: cfg("kube-worker", 5, true), ConfigPlanChecks: empty},
			// Taken out of the pool in the console: the plan adds the group again.
			{PreConfig: func() { clearLB(plugin, "pools", "members") }, Config: cfg("kube-worker", 5, true),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(res, plancheck.ResourceActionCreate)}}},
			// A role the console doesn't offer is refused by the cloud; another role replaces the group.
			{Config: cfg("kube-master", 5, true), ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
				plancheck.ExpectResourceAction(res, plancheck.ResourceActionDestroyBeforeCreate),
			}}, ExpectError: regexp.MustCompile(`not one this load balancer can use`)},
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

// replacePort gives the cluster member another port.
func replacePort(cfg string) string {
	return regexp.MustCompile(`port             = 30080`).ReplaceAllString(cfg, `port             = 30090`)
}
