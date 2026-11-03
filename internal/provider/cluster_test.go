package provider

import (
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/knownvalue"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/statecheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"
)

// clusterStore is an in-memory cluster API: a create is "ok" at once, with nodeCount workers.
type clusterStore struct {
	mu         sync.Mutex
	next       int64
	clusters   map[int64]map[string]any
	workers    map[int64][]int64
	posted     []map[string]any // each create's "cluster"
	added      []map[string]any // each worker add's "server"
	removed    []int64
	scaler     []map[string]any // each configure-autoscaler's "cluster"
	updated    []map[string]any // each PUT's "cluster"
	failScaler int              // the next n configure-autoscaler calls fail with a 400
	noScaler   int              // the next n find no autoscaler installed, as on a cluster still building
	stuck      bool             // new clusters stay "provisioning", as MTN Cloud's did on 2026-11-03
	failAdd    bool             // workers added from now on fail to build
	failed     map[int64]bool   // workers that failed
}

func newClusterStore() *clusterStore {
	return &clusterStore{next: 9600, clusters: map[int64]map[string]any{}, workers: map[int64][]int64{}, failed: map[int64]bool{}}
}

func (s *clusterStore) addWorkers(id int64, n int) {
	for range n {
		s.next++
		s.workers[id] = append(s.workers[id], s.next)
	}
}

func (s *clusterStore) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	reply := func(v any) { _ = json.NewEncoder(w).Encode(v) }
	body := func(key string) map[string]any {
		var b map[string]map[string]any
		_ = json.NewDecoder(r.Body).Decode(&b)
		return b[key]
	}
	parts := strings.Split(strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/clusters"), "/"), "/")
	if parts[0] == "" {
		if r.Method == http.MethodPost {
			c := body("cluster")
			s.posted = append(s.posted, c)
			s.next++
			id := s.next
			layout := c["layout"].(map[string]any)["id"]
			typ := map[string]any{"code": c["type"], "name": map[any]string{"kubernetes-cluster": "Kubernetes Cluster", "docker-cluster": "Docker Cluster"}[c["type"]]}
			s.clusters[id] = map[string]any{"id": id, "name": c["name"], "description": c["description"], "labels": c["labels"],
				"status": "ok", "serviceUrl": "https://10.0.0.10:6443", "layout": map[string]any{"id": layout}, "type": typ}
			if s.stuck {
				s.clusters[id]["status"] = "provisioning"
			}
			nodes, _ := c["worker"].(map[string]any)
			if nodes == nil {
				nodes = c["server"].(map[string]any) // Docker: the hosts are the server part
			}
			s.addWorkers(id, int(nodes["nodeCount"].(float64)))
			reply(map[string]any{"success": true, "cluster": map[string]any{"id": id}})
			return
		}
		var list []any
		for _, c := range s.clusters {
			if n := r.URL.Query().Get("name"); n == "" || c["name"] == n {
				list = append(list, c)
			}
		}
		reply(map[string]any{"clusters": list, "meta": map[string]any{"total": len(list)}})
		return
	}
	id, _ := strconv.ParseInt(parts[0], 10, 64)
	c, ok := s.clusters[id]
	if !ok {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	switch {
	case len(parts) == 1 && r.Method == http.MethodGet:
		reply(map[string]any{"cluster": c})
	case len(parts) == 1 && r.Method == http.MethodPut:
		u := body("cluster")
		s.updated = append(s.updated, u)
		for k, v := range u {
			c[k] = v
		}
		reply(map[string]any{"success": true})
	case len(parts) == 1 && r.Method == http.MethodDelete:
		delete(s.clusters, id)
		reply(map[string]any{"success": true})
	case parts[1] == "workers":
		var list []any
		for _, wid := range s.workers[id] {
			status := "provisioned"
			if s.failed[wid] {
				status = "failed"
			}
			list = append(list, map[string]any{"id": wid, "name": "w-" + strconv.FormatInt(wid, 10), "status": status})
		}
		reply(map[string]any{"workers": list})
	case parts[1] == "servers" && r.Method == http.MethodPost:
		b := body("server")
		if b["serverType"] == nil { // as MTN Cloud answers it (QA cluster 148)
			w.WriteHeader(http.StatusBadRequest)
			reply(map[string]any{"success": false, "msg": "Validation failed", "errors": map[string]any{"serverType": "Server type not specified or not available"}})
			return
		}
		s.added = append(s.added, b)
		s.addWorkers(id, int(b["nodeCount"].(float64)))
		if s.failAdd {
			s.failed[s.next] = true
		}
		reply(map[string]any{"success": true})
	case parts[1] == "servers" && r.Method == http.MethodDelete:
		sid, _ := strconv.ParseInt(parts[2], 10, 64)
		s.removed = append(s.removed, sid)
		var keep []int64
		for _, wid := range s.workers[id] {
			if wid != sid {
				keep = append(keep, wid)
			}
		}
		s.workers[id] = keep
		reply(map[string]any{"success": true})
	case parts[1] == "configure-autoscaler" && s.failScaler > 0:
		s.failScaler--
		w.WriteHeader(http.StatusBadRequest)
		reply(map[string]any{"success": false, "msg": "autoscaler unavailable"})
	case parts[1] == "configure-autoscaler" && s.noScaler > 0:
		s.noScaler--
		w.WriteHeader(http.StatusBadRequest)
		reply(map[string]any{"success": false, "msg": "Unable to find autoscaler package mks-autoscaler-1-0-0-package"})
	case parts[1] == "configure-autoscaler":
		s.scaler = append(s.scaler, body("cluster"))
		reply(map[string]any{"success": true})
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func clusterFakes(t *testing.T) (*fakeAPI, *clusterStore) {
	t.Helper()
	f := newFakeAPI(t)
	nets := newNetStore()
	nets.networks[9350] = map[string]any{"id": 9350, "name": "app-net", "status": "ACTIVE",
		"zonePool": map[string]any{"id": 9201, "name": "web-prod-Acme"}}
	s := newClusterStore()
	f.net, f.clu = nets, s
	return f, s
}

func jsonOf(v any) string { b, _ := json.Marshal(v); return string(b) }

func TestCluster(t *testing.T) {
	f, s := clusterFakes(t)
	cfg := func(extra, count, scaling string) string {
		return f.providerConfig() + `
resource "mtncloud_managed_kubernetes_cluster" "k8s" {
  name          = "apps"
  group         = "MTNNG_CLOUD_AZ_1"
  project       = "web-prod-Acme"
  labels        = ["apps"]
  resource_name = "apps-node"
  hostname      = "apps"
  domain        = "example.com"
  registry      = "harbor"
  workflow      = "Create Linux User Git"
  worker_count  = ` + count + `
` + extra + `
  master = {
    plan             = "G2S4"
    data_volume      = [{ size = 50, datastore = "ENCR_AT_REST" }]
    networks         = ["app-net"]
    security_groups  = ["web"]
  }
  worker = {
    plan = "G4S16"
  }
` + scaling + `}
`
	}
	scaling := "  autoscaling = {\n    min_nodes = 1\n    max_nodes = 4\n  }\n"
	addr := "mtncloud_managed_kubernetes_cluster.k8s"
	check := func(a string, v knownvalue.Check) statecheck.StateCheck {
		return statecheck.ExpectKnownValue(addr, tfjsonpath.New(a), v)
	}
	inPlace := resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(addr, plancheck.ResourceActionUpdate)}}
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories(),
		Steps: []resource.TestStep{
			{
				Config: cfg("", "2", scaling),
				ConfigStateChecks: []statecheck.StateCheck{
					check("id", knownvalue.StringExact("9601")),
					check("layout", knownvalue.StringExact("single")),
					check("labels", knownvalue.ListExact([]knownvalue.Check{knownvalue.StringExact("apps")})),
					check("description", knownvalue.Null()),
					check("cloud", knownvalue.StringExact("MTNNG_CLOUD_AZ_1")),
					check("kubernetes_version", knownvalue.StringExact("1.35.3")),
					check("pod_cidr", knownvalue.StringExact("10.244.0.0/16")),
					check("service_cidr", knownvalue.StringExact("10.96.0.0/12")),
					check("status", knownvalue.StringExact("ok")),
					check("api_url", knownvalue.StringExact("https://10.0.0.10:6443")),
					check("worker_ids", knownvalue.ListSizeExact(2)),
				},
				Check: func(*terraform.State) error {
					s.mu.Lock()
					defer s.mu.Unlock()
					c := s.posted[0]
					srv := c["server"].(map[string]any)
					// The shape of MTN's Manage Kubernetes/CreateCluster.sh.
					node := `"networkInterfaces":[{"network":{"id":9350},"primaryInterface":true}],` +
						`"securityGroups":["11111111-1111-4111-8111-111111111111","22222222-2222-4222-8222-222222222222"],` +
						`"volumes":[{"datastoreId":"auto","id":-1,"name":"root","rootVolume":true,"size":100,"storageType":11},` +
						`{"datastoreId":"414","id":-1,"name":"data-1","rootVolume":false,"size":50,"storageType":11}]`
					for k, want := range map[string]string{
						"layout": `{"id":315}`, "group": `{"id":9101}`, "cloud": `{"id":4}`, "type": `"kubernetes-cluster"`,
						"taskSetId": `61`, "labels": `["MK8S","apps"]`, "description": `"MK8S Cluster"`, "visibility": `"private"`,
						"config": `{"autoScalerEnabled":"on","defaultRepoAccount":"9501","initConfig":"","k8s_version":"1.35.3-1.1",` +
							`"maxNodes":"4","minNodes":"1","podCidr":"10.244.0.0/16","serviceCidr":"10.96.0.0/12","workerCount":2}`,
						"worker": `{"config":{"createUser":true,"noAgent":false,"resourcePoolId":9201},` +
							`"networkInterfaces":[{"network":{"id":9350},"primaryInterface":true}],"nodeCount":2,"plan":{"id":9402},` +
							strings.TrimPrefix(node, `"networkInterfaces":[{"network":{"id":9350},"primaryInterface":true}],`) + `}`,
					} {
						if got := jsonOf(c[k]); got != want {
							t.Errorf("cluster.%s\n got %s\nwant %s", k, got, want)
						}
					}
					// The worker count goes in the server part too, as the platform's CLI sends it: the
					// worker block's alone is ignored (QA cluster 146).
					for k, want := range map[string]string{
						"name": `"apps-node"`, "hostname": `"apps"`, "networkDomain": `{"id":16}`, "plan": `{"id":9401}`, "nodeCount": `2`,
						"sshKeyPair": `null`, "labels": `["MK8S","apps"]`,
						"config": `{"autoScalerEnabled":"on","createUser":true,"customOptions":{"k8s_version":"1.35.3-1.1",` +
							`"podCidr":"10.244.0.0/16","serviceCidr":"10.96.0.0/12"},"maxNodes":"4","minNodes":"1","noAgent":false,` +
							`"nodeCount":2,"resourcePoolId":9201}`,
					} {
						if got := jsonOf(srv[k]); got != want {
							t.Errorf("cluster.server.%s\n got %s\nwant %s", k, got, want)
						}
					}
					if got := jsonOf(s.scaler); got != `[{"autoScalerEnabled":true,"autoScalerGroups":[{"maxNodes":4,"minNodes":1}]}]` {
						t.Errorf("autoscaler: %s", got)
					}
					return nil
				},
			},
			{Config: cfg("", "2", scaling), ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()}}},
			{
				// With autoscaling, the autoscaler owns the count.
				Config: cfg("", "3", scaling), PlanOnly: true, ExpectError: regexp.MustCompile(`Worker count is set by the autoscaler`),
			},
			{
				// Autoscaling off, one more worker with the worker settings, and a description: all in place.
				Config: cfg(`  description = "apps cluster"`, "3", ""), ConfigPlanChecks: inPlace,
				ConfigStateChecks: []statecheck.StateCheck{check("worker_ids", knownvalue.ListSizeExact(3))},
				Check: func(*terraform.State) error {
					s.mu.Lock()
					defer s.mu.Unlock()
					a := s.added[0]
					// The count added goes outside config only: the create's server.config.nodeCount isn't sent.
					cfg, _ := a["config"].(map[string]any)
					if jsonOf(a["nodeCount"]) != "1" || jsonOf(a["plan"]) != `{"id":9402}` || jsonOf(a["cloud"]) != `{"id":4}` || cfg["nodeCount"] != nil ||
						jsonOf(a["serverType"]) != `{"id":137}` {
						t.Errorf("worker add: %s", jsonOf(a))
					}
					if got := jsonOf(s.updated[0]); got != `{"description":"apps cluster","labels":["MK8S","apps"],"name":"apps"}` {
						t.Errorf("update: %s", got)
					}
					return nil
				},
			},
			{
				// Down to one: the newest two go. Without autoscaling, the autoscaler is held at the count.
				Config: cfg(`  description = "apps cluster"`, "1", ""), ConfigPlanChecks: inPlace,
				Check: func(*terraform.State) error {
					s.mu.Lock()
					defer s.mu.Unlock()
					if got := jsonOf(s.removed); got != "[9604,9603]" {
						t.Errorf("removed %s, want the newest two", got)
					}
					if got := jsonOf(s.scaler[len(s.scaler)-1]); got != `{"autoScalerEnabled":true,"autoScalerGroups":[{"maxNodes":1,"minNodes":1}]}` {
						t.Errorf("autoscaler: %s", got)
					}
					return nil
				},
			},
			{Config: cfg(`  description = "apps cluster"`, "1", ""), ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()}}},
			{
				// What the nodes are built with can't change in place.
				Config: cfg(`  description = "apps cluster"`+"\n  pod_cidr = \"10.200.0.0/16\"", "1", ""), PlanOnly: true, ExpectNonEmptyPlan: true,
			},
			{
				Config: cfg(`  description = "apps cluster"`, "1", ""), ResourceName: addr, ImportState: true, ImportStateVerify: true,
				ImportStateVerifyIgnore: []string{"group", "project", "cloud", "resource_name", "hostname", "domain", "registry", "workflow",
					"kubernetes_version", "pod_cidr", "service_cidr", "master", "worker", "timeouts"},
			},
		},
		CheckDestroy: func(*terraform.State) error {
			s.mu.Lock()
			defer s.mu.Unlock()
			if len(s.clusters) != 0 {
				t.Errorf("%d clusters left behind", len(s.clusters))
			}
			return nil
		},
	})
}

// Every name is checked when you plan, and nothing is built.
func TestCluster_CheckedAtPlan(t *testing.T) {
	f, s := clusterFakes(t)
	cfg := func(extra, masterPlan string) string {
		return f.providerConfig() + `
resource "mtncloud_managed_kubernetes_cluster" "k8s" {
  name    = "apps"
  group   = "MTNNG_CLOUD_AZ_1"
  project = "web-prod-Acme"
` + extra + `
  master = {
    plan             = "` + masterPlan + `"
    data_volume      = [{ size = 50 }]
    networks         = ["app-net"]
  }
}
`
	}
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories(),
		Steps: []resource.TestStep{
			{Config: cfg(`  layout = "HKS Kubernetes 1.35 Cluster on Ubuntu 24.04"`, "G2S4"), PlanOnly: true,
				ExpectError: regexp.MustCompile(`must be one of: \["single" "ha3" "ha5"\]`)},
			{Config: cfg(`  description = ""`, "G2S4"), PlanOnly: true, ExpectError: regexp.MustCompile(`length must be at least 1`)},
			{Config: cfg("", "G9S9"), PlanOnly: true, ExpectError: regexp.MustCompile(`no plan "G9S9"(.|\n)*G4S16`)},
			{Config: cfg(`  kubernetes_version = "1.20"`, "G2S4"), PlanOnly: true, ExpectError: regexp.MustCompile(`1\.35\.3`)},
			{Config: cfg(`  registry = "quay"`, "G2S4"), PlanOnly: true, ExpectError: regexp.MustCompile(`no registry "quay"`)},
			{Config: cfg("  worker_count = 6\n  autoscaling = {\n    min_nodes = 1\n    max_nodes = 4\n  }", "G2S4"), PlanOnly: true,
				ExpectError: regexp.MustCompile(`between min_nodes 1 and max_nodes 4`)},
		},
	})
	if len(s.posted) != 0 {
		t.Errorf("%d clusters built", len(s.posted))
	}
}

// A Docker cluster: MTN's Docker layout, its hosts from host, none of the Kubernetes parts; hosts
// are added in place.
func TestDockerCluster(t *testing.T) {
	f, s := clusterFakes(t)
	cfg := func(count string) string {
		return f.providerConfig() + `
resource "mtncloud_docker_cluster" "docker" {
  name       = "builds"
  group      = "MTNNG_CLOUD_AZ_1"
  project    = "web-prod-Acme"
  host_count = ` + count + `
  host = {
    plan            = "G2S4"
    data_volume     = [{ size = 100 }]
    networks        = ["app-net"]
    security_groups = ["web"]
  }
}
`
	}
	addr := "mtncloud_docker_cluster.docker"
	check := func(a string, v knownvalue.Check) statecheck.StateCheck {
		return statecheck.ExpectKnownValue(addr, tfjsonpath.New(a), v)
	}
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories(),
		Steps: []resource.TestStep{
			{
				Config: cfg("1"),
				ConfigStateChecks: []statecheck.StateCheck{
					check("host_count", knownvalue.Int64Exact(1)),
					check("host_ids", knownvalue.ListSizeExact(1)),
					check("status", knownvalue.StringExact("ok")),
				},
				Check: func(*terraform.State) error {
					s.mu.Lock()
					defer s.mu.Unlock()
					c := s.posted[0]
					if jsonOf(c["type"]) != `"docker-cluster"` || jsonOf(c["layout"]) != `{"id":311}` || c["worker"] != nil ||
						c["config"] != nil || c["labels"] != nil || c["description"] != nil {
						t.Errorf("docker cluster: %s", jsonOf(c))
					}
					srv := c["server"].(map[string]any)
					for k, want := range map[string]string{
						"nodeCount": `1`, "sshKeyPair": `null`,
						"securityGroups": `["11111111-1111-4111-8111-111111111111","22222222-2222-4222-8222-222222222222"]`,
						"volumes":        `[{"datastoreId":"auto","id":-1,"name":"root","rootVolume":true,"size":100,"storageType":11},{"datastoreId":"auto","id":-1,"name":"data-1","rootVolume":false,"size":100,"storageType":11}]`,
						"config":         `{"createUser":true,"noAgent":false,"nodeCount":1,"resourcePoolId":9201}`,
					} {
						if got := jsonOf(srv[k]); got != want {
							t.Errorf("cluster.server.%s\n got %s\nwant %s", k, got, want)
						}
					}
					return nil
				},
			},
			{Config: cfg("1"), ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()}}},
			{
				// One more host, in place, built like the first; no autoscaler on a Docker cluster.
				Config: cfg("2"),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction(addr, plancheck.ResourceActionUpdate)}},
				ConfigStateChecks: []statecheck.StateCheck{check("host_ids", knownvalue.ListSizeExact(2))},
				Check: func(*terraform.State) error {
					s.mu.Lock()
					defer s.mu.Unlock()
					a := s.added[0]
					if jsonOf(a["nodeCount"]) != "1" || jsonOf(a["plan"]) != `{"id":9401}` || a["sshKeyPair"] != nil ||
						jsonOf(a["serverType"]) != `{"id":135}` ||
						a["config"].(map[string]any)["nodeCount"] != nil {
						t.Errorf("host add: %s", jsonOf(a))
					}
					if len(s.scaler) != 0 {
						t.Errorf("autoscaler configured on a Docker cluster: %s", jsonOf(s.scaler))
					}
					return nil
				},
			},
			{Config: cfg("2"), ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()}}},
			{
				Config: cfg("2"), ResourceName: addr, ImportState: true, ImportStateVerify: true,
				ImportStateVerifyIgnore: []string{"group", "project", "cloud", "host", "timeouts"},
			},
		},
	})
}

// A cluster still building when the create's timeout runs out fails the apply (D-043). It stays in state,
// tainted, so the next apply replaces it.
func TestDockerCluster_TimeoutFails(t *testing.T) {
	f, s := clusterFakes(t)
	s.stuck = true
	cfg := f.providerConfig() + `
resource "mtncloud_docker_cluster" "docker" {
  name       = "builds"
  group      = "MTNNG_CLOUD_AZ_1"
  project    = "web-prod-Acme"
  host_count = 1
  host = {
    plan            = "G2S4"
    data_volume     = [{ size = 100 }]
    networks        = ["app-net"]
    security_groups = ["web"]
  }
  timeouts {
    create = "5s"
  }
}
`
	addr := "mtncloud_docker_cluster.docker"
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories(),
		Steps: []resource.TestStep{
			{Config: cfg, ExpectError: regexp.MustCompile(`(?s)Cluster not healthy in time.*status "provisioning",\s+1\s+of\s+1\s+hosts\s+ready`)},
			{
				// Kept in state, tainted: the next apply replaces it.
				PreConfig: func() { s.mu.Lock(); s.stuck = false; s.mu.Unlock() },
				Config:    cfg,
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction(addr, plancheck.ResourceActionDestroyBeforeCreate)}},
			},
		},
	})
	if len(s.posted) != 2 {
		t.Errorf("%d clusters built, want 2: the stuck one, then its replacement", len(s.posted))
	}
}

// A cluster that isn't healthy can't be changed: the plan says so, and nothing is sent. A destroy still
// works.
func TestCluster_ChangesNeedHealthy(t *testing.T) {
	f, s := clusterFakes(t)
	cfg := func(desc string) string {
		return clusterConfig(f, "  worker_count = 1\n  description = \""+desc+"\"", "")
	}
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories(),
		Steps: []resource.TestStep{
			{Config: cfg("one")},
			{
				PreConfig:   func() { s.mu.Lock(); s.clusters[9601]["status"] = "warning"; s.mu.Unlock() },
				Config:      cfg("two"),
				ExpectError: regexp.MustCompile(`(?s)Cluster not healthy.*Cluster 9601 is "warning"`),
			},
			{Config: cfg("one"), PlanOnly: true}, // no change: nothing to refuse
		},
	})
	if len(s.updated) != 0 {
		t.Errorf("updates sent to an unhealthy cluster: %s", jsonOf(s.updated))
	}
}

// Scaling is done only when the new workers are ready: one that fails ends the apply with its name. It
// doesn't count, so the next plan shows the cluster short, and its apply replaces the failed worker.
func TestCluster_ScaleWaitsForWorkers(t *testing.T) {
	f, s := clusterFakes(t)
	cfg := func(n string) string { return clusterConfig(f, "  worker_count = "+n, "") }
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories(),
		Steps: []resource.TestStep{
			{Config: cfg("1")},
			{
				PreConfig:   func() { s.mu.Lock(); s.failAdd = true; s.mu.Unlock() },
				Config:      cfg("2"),
				ExpectError: regexp.MustCompile(`(?s)Cluster failed.*Failed workers: w-9603`),
			},
			{
				// The failed worker doesn't count: the plan shows the cluster short again.
				Config: cfg("2"), PlanOnly: true, ExpectNonEmptyPlan: true,
			},
			{
				// The apply removes the failed worker and builds a new one.
				PreConfig: func() { s.mu.Lock(); s.failAdd = false; s.mu.Unlock() },
				Config:    cfg("2"),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue("mtncloud_managed_kubernetes_cluster.k8s", tfjsonpath.New("worker_ids"),
						knownvalue.ListExact([]knownvalue.Check{knownvalue.StringExact("9602"), knownvalue.StringExact("9604")})),
				},
				Check: func(*terraform.State) error {
					s.mu.Lock()
					defer s.mu.Unlock()
					if got := jsonOf(s.removed); got != "[9603]" {
						t.Errorf("removed %s, want the failed worker", got)
					}
					return nil
				},
			},
			{Config: cfg("2"), ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()}}},
		},
	})
}

// Each resource manages its own kind only: a Kubernetes cluster can't be imported as a Docker one.
func TestDockerCluster_WrongKind(t *testing.T) {
	f, s := clusterFakes(t)
	s.clusters[9700] = map[string]any{"id": 9700, "name": "apps", "status": "ok", "layout": map[string]any{"id": 315},
		"type": map[string]any{"code": "kubernetes-cluster", "name": "Kubernetes Cluster"}}
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories(),
		Steps: []resource.TestStep{{
			Config: f.providerConfig() + `
import {
  to = mtncloud_docker_cluster.d
  id = "9700"
}
resource "mtncloud_docker_cluster" "d" {
  name = "apps"
  host = {
    plan        = "G2S4"
    data_volume = [{ size = 100 }]
    networks    = ["app-net"]
  }
}
`,
			PlanOnly: true, ExpectError: regexp.MustCompile(`not a Docker Cluster`),
		}},
	})
}

func clusterConfig(f *fakeAPI, top, scaling string) string {
	return f.providerConfig() + `
resource "mtncloud_managed_kubernetes_cluster" "k8s" {
  name    = "apps"
  group   = "MTNNG_CLOUD_AZ_1"
  project = "web-prod-Acme"
` + top + `
  master = {
    plan             = "G2S4"
    data_volume      = [{ size = 50 }]
    networks         = ["app-net"]
  }
` + scaling + `}
`
}

// The autoscaler moves the workers; worker_count holds. Turning it off brings the count back.
func TestCluster_AutoscalerMovesWorkers(t *testing.T) {
	f, s := clusterFakes(t)
	scaling := "  autoscaling = {\n    min_nodes = 1\n    max_nodes = 4\n  }\n"
	addr := "mtncloud_managed_kubernetes_cluster.k8s"
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories(),
		Steps: []resource.TestStep{
			{Config: clusterConfig(f, "  worker_count = 2", scaling)},
			{
				PreConfig:        func() { s.mu.Lock(); s.addWorkers(9601, 2); s.mu.Unlock() },
				Config:           clusterConfig(f, "  worker_count = 2", scaling),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()}},
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(addr, tfjsonpath.New("worker_count"), knownvalue.Int64Exact(2)),
					statecheck.ExpectKnownValue(addr, tfjsonpath.New("worker_ids"), knownvalue.ListSizeExact(4)),
				},
			},
			{
				// The count in the configuration is unchanged, but there are 4 workers: back to 2.
				Config: clusterConfig(f, "  worker_count = 2", ""),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction(addr, plancheck.ResourceActionUpdate)}},
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(addr, tfjsonpath.New("worker_ids"), knownvalue.ListSizeExact(2)),
				},
			},
			{Config: clusterConfig(f, "  worker_count = 2", ""), ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()}}},
		},
	})
}

// What you type is kept: a version given as its value.
func TestCluster_KeepsWhatYouTyped(t *testing.T) {
	f, s := clusterFakes(t)
	cfg := clusterConfig(f, "  layout = \"single\"\n  kubernetes_version = \"1.35.3-1.1\"", "")
	addr := "mtncloud_managed_kubernetes_cluster.k8s"
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories(),
		Steps: []resource.TestStep{
			{
				Config: cfg,
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(addr, tfjsonpath.New("layout"), knownvalue.StringExact("single")),
					statecheck.ExpectKnownValue(addr, tfjsonpath.New("kubernetes_version"), knownvalue.StringExact("1.35.3-1.1")),
				},
				Check: func(*terraform.State) error {
					s.mu.Lock()
					defer s.mu.Unlock()
					custom := s.posted[0]["server"].(map[string]any)["config"].(map[string]any)["customOptions"].(map[string]any)
					if custom["k8s_version"] != "1.35.3-1.1" {
						t.Errorf("k8s_version sent %v", custom["k8s_version"])
					}
					return nil
				},
			},
			{Config: cfg, ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()}}},
		},
	})
}

// A worker count only known after apply stays unknown in the plan.
func TestCluster_CountKnownAfterApply(t *testing.T) {
	f, s := clusterFakes(t)
	cfg := `
resource "terraform_data" "n" {
  input = 2
}
` + clusterConfig(f, "  worker_count = terraform_data.n.output", "")
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories(),
		Steps: []resource.TestStep{
			{
				Config: cfg,
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue("mtncloud_managed_kubernetes_cluster.k8s", tfjsonpath.New("worker_ids"), knownvalue.ListSizeExact(2)),
				},
			},
			{Config: cfg, ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()}}},
		},
	})
	if len(s.posted) != 1 {
		t.Errorf("%d clusters built", len(s.posted))
	}
}

// An autoscaler that fails after the build warns, never taints: the next apply configures it in place.
// A cluster deleted out of band is planned again.
func TestCluster_AutoscalerFailsAndDisappears(t *testing.T) {
	f, s := clusterFakes(t)
	s.failScaler = 1
	cfg := clusterConfig(f, "", "  autoscaling = {\n    min_nodes = 1\n    max_nodes = 4\n  }\n")
	addr := "mtncloud_managed_kubernetes_cluster.k8s"
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories(),
		Steps: []resource.TestStep{
			{
				Config: cfg, ExpectNonEmptyPlan: true,
				Check: func(st *terraform.State) error {
					if st.RootModule().Resources[addr].Primary.Tainted {
						t.Error("the cluster was tainted")
					}
					return nil
				},
			},
			{
				Config: cfg,
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction(addr, plancheck.ResourceActionUpdate)}},
				Check: func(*terraform.State) error {
					s.mu.Lock()
					defer s.mu.Unlock()
					if len(s.posted) != 1 || len(s.scaler) != 1 {
						t.Errorf("built %d, autoscaler configured %d times; want 1 and 1", len(s.posted), len(s.scaler))
					}
					return nil
				},
			},
			{
				PreConfig: func() { s.mu.Lock(); delete(s.clusters, 9601); s.mu.Unlock() },
				Config:    cfg, PlanOnly: true, ExpectNonEmptyPlan: true,
			},
		},
	})
}

// Without autoscaling the autoscaler is still sent on, held at worker_count (MTN's order: a cluster
// built with it off never finishes its workers), and moves with the count. The root disk is a fixed
// 100 GB, and three workers are built, as MTN's order does.
func TestCluster_AutoscalerHeldAtCount(t *testing.T) {
	f, s := clusterFakes(t)
	cfg := func(top string) string {
		return f.providerConfig() + `
resource "mtncloud_managed_kubernetes_cluster" "k8s" {
  name    = "apps"
  group   = "MTNNG_CLOUD_AZ_1"
  project = "web-prod-Acme"
` + top + `
  master = {
    plan        = "G2S4"
    data_volume = [{ size = 50 }]
    networks    = ["app-net"]
  }
}
`
	}
	addr := "mtncloud_managed_kubernetes_cluster.k8s"
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories(),
		Steps: []resource.TestStep{
			{
				Config: cfg(""),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(addr, tfjsonpath.New("worker_count"), knownvalue.Int64Exact(3)),
					statecheck.ExpectKnownValue(addr, tfjsonpath.New("worker_ids"), knownvalue.ListSizeExact(3)),
					statecheck.ExpectKnownValue(addr, tfjsonpath.New("layout"), knownvalue.StringExact("single")),
				},
				Check: func(*terraform.State) error {
					s.mu.Lock()
					defer s.mu.Unlock()
					c := s.posted[0]
					top := c["config"].(map[string]any)
					if top["autoScalerEnabled"] != "on" || top["minNodes"] != "3" || top["maxNodes"] != "3" || top["workerCount"] != float64(3) {
						t.Errorf("config: %s", jsonOf(top))
					}
					srv := c["server"].(map[string]any)
					if got := jsonOf(srv["volumes"]); got != `[{"datastoreId":"auto","id":-1,"name":"root","rootVolume":true,"size":100,"storageType":11},{"datastoreId":"auto","id":-1,"name":"data-1","rootVolume":false,"size":50,"storageType":11}]` {
						t.Errorf("volumes: %s", got)
					}
					if got := jsonOf(c["worker"].(map[string]any)["volumes"]); got != jsonOf(srv["volumes"]) {
						t.Errorf("worker volumes %s, want the master's", got)
					}
					if len(s.scaler) != 0 {
						t.Errorf("autoscaler configured after the build: %s", jsonOf(s.scaler))
					}
					return nil
				},
			},
			{Config: cfg(""), ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()}}},
			{
				// The autoscaler moves to the new count before the worker is added, so it doesn't undo it.
				Config: cfg("  worker_count = 4"),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(addr, tfjsonpath.New("worker_ids"), knownvalue.ListSizeExact(4)),
				},
				Check: func(*terraform.State) error {
					s.mu.Lock()
					defer s.mu.Unlock()
					if got := jsonOf(s.scaler); got != `[{"autoScalerEnabled":true,"autoScalerGroups":[{"maxNodes":4,"minNodes":4}]}]` {
						t.Errorf("autoscaler: %s", got)
					}
					return nil
				},
			},
			{Config: cfg("  worker_count = 4"), ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()}}},
		},
	})
}

// MTN Cloud refusing the autoscaler change because its software isn't on the cluster (QA cluster 151,
// 2026-11-03) stops the scaling with a plain error; the next apply finishes the change.
func TestCluster_ScaleNeedsAutoscaler(t *testing.T) {
	f, s := clusterFakes(t)
	cfg := func(top string) string {
		return f.providerConfig() + `
resource "mtncloud_managed_kubernetes_cluster" "k8s" {
  name    = "apps"
  group   = "MTNNG_CLOUD_AZ_1"
  project = "web-prod-Acme"
` + top + `
  master = {
    plan        = "G2S4"
    data_volume = [{ size = 50 }]
    networks    = ["app-net"]
  }
}
`
	}
	addr := "mtncloud_managed_kubernetes_cluster.k8s"
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories(),
		Steps: []resource.TestStep{
			{Config: cfg("  worker_count = 1")},
			{
				PreConfig:   func() { s.mu.Lock(); s.noScaler = 1; s.mu.Unlock() },
				Config:      cfg("  worker_count = 2"),
				ExpectError: regexp.MustCompile(`(?s)Unable to hold the autoscaler.*hasn't installed the autoscaler on\s+cluster 9601`),
			},
			{
				Config: cfg("  worker_count = 2"),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(addr, tfjsonpath.New("worker_ids"), knownvalue.ListSizeExact(2)),
				},
				Check: func(*terraform.State) error {
					s.mu.Lock()
					defer s.mu.Unlock()
					if len(s.added) != 1 {
						t.Errorf("%d adds, want 1: none before the autoscaler was held", len(s.added))
					}
					return nil
				},
			},
			{Config: cfg("  worker_count = 2"), ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()}}},
		},
	})
}

// With autoscaling set, a refused range change is an error; the next apply stores it.
func TestCluster_AutoscalingWaitsForAutoscaler(t *testing.T) {
	f, s := clusterFakes(t)
	scaling := func(hi int) string {
		return clusterConfig(f, "", fmt.Sprintf("  autoscaling = {\n    min_nodes = 1\n    max_nodes = %d\n  }\n", hi))
	}
	addr := "mtncloud_managed_kubernetes_cluster.k8s"
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories(),
		Steps: []resource.TestStep{
			{Config: scaling(4)},
			{
				PreConfig:   func() { s.mu.Lock(); s.noScaler, s.scaler = 1, nil; s.mu.Unlock() },
				Config:      scaling(5),
				ExpectError: regexp.MustCompile(`Unable to configure the autoscaler`),
			},
			{
				Config: scaling(5),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction(addr, plancheck.ResourceActionUpdate)}},
				Check: func(*terraform.State) error {
					s.mu.Lock()
					defer s.mu.Unlock()
					if got := jsonOf(s.scaler); got != `[{"autoScalerEnabled":true,"autoScalerGroups":[{"maxNodes":5,"minNodes":1}]}]` {
						t.Errorf("autoscaler: %s", got)
					}
					return nil
				},
			},
			{Config: scaling(5), ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()}}},
		},
	})
}

// An unhealthy cluster can still be rebuilt: a change that builds a new cluster plans a replacement even
// with an in-place change beside it (review, 2026-11-03).
func TestCluster_UnhealthyCanBeReplaced(t *testing.T) {
	f, s := clusterFakes(t)
	cfg := func(extra string) string {
		return clusterConfig(f, "  worker_count = 1\n  description = \"two\"\n"+extra, "")
	}
	addr := "mtncloud_managed_kubernetes_cluster.k8s"
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories(),
		Steps: []resource.TestStep{
			{Config: clusterConfig(f, "  worker_count = 1", "")},
			{
				PreConfig: func() { s.mu.Lock(); s.clusters[9601]["status"] = "warning"; s.mu.Unlock() },
				Config:    cfg(`  pod_cidr = "10.200.0.0/16"`), PlanOnly: true, ExpectNonEmptyPlan: true,
			},
			{
				Config: cfg(`  pod_cidr = "10.200.0.0/16"`),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction(addr, plancheck.ResourceActionDestroyBeforeCreate)}},
			},
		},
	})
	if len(s.posted) != 2 {
		t.Errorf("%d clusters built, want 2", len(s.posted))
	}
}

// The apply checks health again on a fresh read: an imported cluster's first plan isn't checked, and the
// plan's status can be old.
func TestCluster_ApplyChecksHealth(t *testing.T) {
	f, s := clusterFakes(t)
	s.clusters[9700] = map[string]any{"id": 9700, "name": "apps", "status": "ok", "layout": map[string]any{"id": 315},
		"type": map[string]any{"code": "kubernetes-cluster", "name": "Kubernetes Cluster"}}
	s.addWorkers(9700, 1)
	cfg := func(name string) string {
		return strings.Replace(clusterConfig(f, "  worker_count = 1", ""), `name    = "apps"`, `name    = "`+name+`"`, 1)
	}
	addr := "mtncloud_managed_kubernetes_cluster.k8s"
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories(),
		Steps: []resource.TestStep{
			{Config: cfg("apps"), ResourceName: addr, ImportState: true, ImportStateId: "9700", ImportStatePersist: true},
			{
				PreConfig:   func() { s.mu.Lock(); s.clusters[9700]["status"] = "provisioning"; s.mu.Unlock() },
				Config:      cfg("apps2"),
				ExpectError: regexp.MustCompile(`(?s)Cluster not healthy.*Cluster 9700 is "provisioning"`),
			},
			{
				PreConfig: func() { s.mu.Lock(); s.clusters[9700]["status"] = "ok"; s.mu.Unlock() },
				Config:    cfg("apps2"),
			},
		},
	})
	if len(s.updated) != 1 {
		t.Errorf("%d updates, want 1: none while it wasn't healthy", len(s.updated))
	}
}
