package provider

import (
	"context"
	"fmt"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
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

// dockerGroup is Docker cluster 144's hosts as the load balancer names them on the wire.
const dockerGroup = "cluster:144:morpheus-node" // mtncloud:allow-upstream-name (the platform's role code on the wire)

// A Docker cluster's hosts are role = "docker-worker", sent as the cloud's own code, which a customer
// never sees (owner, 2026-11-19): a configuration that writes it is refused at plan time, MTN's
// errors and the state say docker-worker, and an import by the console's spelling stores docker-worker.
func TestLoadBalancerClusterMember_DockerWorker(t *testing.T) {
	f := newFakeAPI(t)
	plugin := newLBPlugin()
	f.lb = plugin
	wire := dockerGroup[len("cluster:144:"):]
	cfgFor := func(name string, cluster int, role string, port int) string {
		return fmt.Sprintf(`
resource "mtncloud_load_balancer_cluster_member" %q {
  network_id       = 114
  load_balancer_id = mtncloud_load_balancer.web.id
  pool_id          = mtncloud_load_balancer_pool.web.id
  cluster_id       = %d
  role             = %q
  port             = %d
}
`, name, cluster, role, port)
	}
	cfg := func(role string) string { return lbBase(f) + cfgFor("docker", 144, role, 8080) }
	res := "mtncloud_load_balancer_cluster_member.docker"
	empty := resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()}}
	adds := func(want int) func(*terraform.State) error {
		return func(*terraform.State) error {
			plugin.mu.Lock()
			defer plugin.mu.Unlock()
			if n := plugin.runs["member-add"]; n != want {
				return fmt.Errorf("%d member adds, want %d", n, want)
			}
			if len(plugin.groups) == 0 || plugin.groups[0] != dockerGroup {
				return fmt.Errorf("sent cluster groups %q, want %q first", plugin.groups, dockerGroup)
			}
			return nil
		}
	}
	importID := func(role string) func(*terraform.State) (string, error) {
		return func(s *terraform.State) (string, error) {
			a := s.RootModule().Resources[res].Primary.Attributes
			return fmt.Sprintf("114/%s/%s/144:%s", a["load_balancer_id"], a["pool_id"], role), nil
		}
	}
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories(),
		Steps: []resource.TestStep{
			// The cloud's own code in a configuration: refused at plan time, before anything is sent.
			{Config: cfg(wire), ExpectError: regexp.MustCompile(`(?s)Use docker-worker.*role = "docker-worker"`)},
			{Config: cfg("docker-worker"), Check: adds(1), ConfigStateChecks: []statecheck.StateCheck{
				statecheck.ExpectKnownValue(res, tfjsonpath.New("id"), knownvalue.StringExact("144:docker-worker")),
				statecheck.ExpectKnownValue(res, tfjsonpath.New("role"), knownvalue.StringExact("docker-worker")),
				statecheck.ExpectKnownValue(res, tfjsonpath.New("members"), knownvalue.ListSizeExact(3)),
			}},
			{Config: cfg("docker-worker"), ConfigPlanChecks: empty},
			// A second owner of the group is matched on the cloud's code: refused before anything is added.
			{Config: cfg("docker-worker") + cfgFor("again", 144, "docker-worker", 8081),
				ExpectError: regexp.MustCompile(`Cluster role already in the pool`)},
			{Config: cfg("docker-worker"), ConfigPlanChecks: empty, Check: adds(1)},
			// MTN's own refusal names the group by the cloud's code: the customer reads docker-worker.
			{Config: cfg("docker-worker") + cfgFor("other", 145, "docker-worker", 8082),
				ExpectError: regexp.MustCompile(`Cluster group cluster:145:docker-worker is not one`)},
			{Config: cfg("docker-worker"), ConfigPlanChecks: empty},
			// Import takes the console's spelling too, and stores docker-worker: the plan stays clean.
			{ResourceName: res, ImportState: true, ImportStateVerify: true, ImportStateVerifyIgnore: []string{"timeouts"},
				ImportStateIdFunc: importID("docker-worker")},
			{ResourceName: res, ImportState: true, ImportStateIdFunc: importID(wire),
				ImportStateCheck: func(s []*terraform.InstanceState) error {
					if len(s) != 1 || s[0].Attributes["role"] != "docker-worker" || s[0].ID != "144:docker-worker" || s[0].Attributes["members.#"] != "3" {
						return fmt.Errorf("imported %v", s)
					}
					return nil
				}},
		},
		// Destroy removes the group by the cloud's code: no member left behind.
		CheckDestroy: func(*terraform.State) error {
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
		},
	})
}

// A 0.11.2 state of Docker hosts (schema version 0, the cloud's code in role and id) comes out of
// the provider's state upgrade, which Terraform runs before any refresh or plan, as docker-worker.
func TestLoadBalancerClusterMember_UpgradeFrom0112(t *testing.T) {
	ctx := context.Background()
	s, err := factories()["mtncloud"]()
	if err != nil {
		t.Fatal(err)
	}
	wire := dockerGroup[len("cluster:144:"):]
	raw := `{"id":"144:` + wire + `","network_id":114,"load_balancer_id":"lb","pool_id":"pool","cluster_id":144,"role":"` + wire +
		`","port":8080,"weight":1,"backup":false,"members":[{"id":"m1","address":"10.0.0.41"}],"timeouts":null}`
	resp, err := s.UpgradeResourceState(ctx, &tfprotov6.UpgradeResourceStateRequest{
		TypeName: "mtncloud_load_balancer_cluster_member", Version: 0, RawState: &tfprotov6.RawState{JSON: []byte(raw)}})
	if err != nil || len(resp.Diagnostics) > 0 || resp.UpgradedState == nil {
		t.Fatalf("upgrade: %v %v", err, resp)
	}
	ps, err := s.GetProviderSchema(ctx, &tfprotov6.GetProviderSchemaRequest{})
	if err != nil {
		t.Fatal(err)
	}
	typ := ps.ResourceSchemas["mtncloud_load_balancer_cluster_member"].ValueType()
	v, err := resp.UpgradedState.Unmarshal(typ)
	if err != nil {
		t.Fatal(err)
	}
	var obj map[string]tftypes.Value
	if err := v.As(&obj); err != nil {
		t.Fatal(err)
	}
	var role, id string
	_ = obj["role"].As(&role)
	_ = obj["id"].As(&id)
	if role != "docker-worker" || id != "144:docker-worker" {
		t.Errorf("upgraded role %q, id %q: want docker-worker", role, id)
	}
}
