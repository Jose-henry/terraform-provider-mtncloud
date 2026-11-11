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
	scaler     []map[string]any // each configure-autoscaler call: the provider never makes one
	updated    []map[string]any // each PUT's "cluster"
	syncDefect bool             // new clusters end "error", "No kube masters found", built fine (clusters 143-171)
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
	if r.URL.Path == "/api/processes" {
		// A failed worker's history, as MTN Cloud's read it: the join script ran with no join command
		// (QA cluster 177's worker, 2026-11-11).
		id, _ := strconv.ParseInt(r.URL.Query().Get("serverId"), 10, 64)
		procs := []any{}
		if s.failed[id] {
			procs = append(procs, map[string]any{"status": "failed", "dateCreated": "2026-11-11T00:17:13Z",
				"processType": map[string]any{"name": "server provision"}, "error": "bash: line 1: null: command not found\n",
				"events": []any{
					map[string]any{"status": "complete", "description": "kubernetes ubuntu setup script v1", "processType": map[string]any{"name": "execute script"}},
					map[string]any{"status": "failed", "description": "kubeadm worker join", "processType": map[string]any{"name": "execute script"},
						"error": "bash: line 1: null: command not found\n"},
					map[string]any{"status": "pending", "description": "setup-ceph-drivers.sh", "processType": map[string]any{"name": "execute script"}},
				}})
		}
		reply(map[string]any{"processes": procs})
		return
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
			if s.syncDefect {
				s.clusters[id]["status"], s.clusters[id]["statusMessage"] = "error", "No kube masters found"
				s.clusters[id]["provisionComplete"] = true
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
		reply(map[string]any{"workers": list, "meta": map[string]any{"total": len(list)}})
	case parts[1] == "masters":
		reply(map[string]any{"masters": []any{map[string]any{"id": id*10 + 1, "name": "m", "status": "provisioned"}}, "meta": map[string]any{"total": 1}})
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
	case parts[1] == "configure-autoscaler":
		// MTN's clusters don't have the platform's autoscaler package (QA cluster 183, 2026-11-11).
		s.scaler = append(s.scaler, body("cluster"))
		w.WriteHeader(http.StatusBadRequest)
		reply(map[string]any{"success": false, "msg": "Unable to find autoscaler package mks-autoscaler-1-0-0-package"})
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
				Config: cfg("", "3", scaling),
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
					check("worker_ids", knownvalue.ListSizeExact(3)),
				},
				Check: func(*terraform.State) error {
					s.mu.Lock()
					defer s.mu.Unlock()
					c := s.posted[0]
					srv := c["server"].(map[string]any)
					// The shape of MTN's Manage Kubernetes/CreateCluster.sh.
					node := `"networkInterfaces":[{"network":{"id":9350},"primaryInterface":true}],` +
						`"securityGroups":["11111111-1111-4111-8111-111111111111","22222222-2222-4222-8222-222222222222"],` +
						`"volumes":[{"datastoreId":"auto","displayOrder":0,"id":-1,"name":"root","rootVolume":true,"size":100,"sizeBytes":107374182400,"storageType":11},` +
						`{"datastoreId":"414","displayOrder":1,"id":-1,"name":"etcd-data","rootVolume":false,"size":50,"sizeBytes":53687091200,"storageType":11}]`
					for k, want := range map[string]string{
						"layout": `{"id":315}`, "group": `{"id":9101}`, "cloud": `{"id":4}`, "type": `"kubernetes-cluster"`,
						"taskSetId": `61`, "labels": `["MK8S","apps"]`, "description": `"MK8S Cluster"`, "visibility": `"private"`,
						"config": `{"autoScalerEnabled":"on","defaultRepoAccount":"9501","initConfig":"","k8s_version":"1.35.3-1.1",` +
							`"maxNodes":"4","minNodes":"1","podCidr":"10.244.0.0/16","serviceCidr":"10.96.0.0/12","workerCount":3}`,
						"worker": `{"config":{"createUser":true,"noAgent":false,"resourcePoolId":9201},` +
							`"networkInterfaces":[{"network":{"id":9350},"primaryInterface":true}],"nodeCount":3,"plan":{"id":9402},` +
							strings.ReplaceAll(strings.TrimPrefix(node, `"networkInterfaces":[{"network":{"id":9350},"primaryInterface":true}],`), `"etcd-data"`, `"data"`) + `}`,
					} {
						if got := jsonOf(c[k]); got != want {
							t.Errorf("cluster.%s\n got %s\nwant %s", k, got, want)
						}
					}
					// The worker count goes in the server part too, as the platform's CLI sends it: the
					// worker block's alone is ignored (QA cluster 146).
					for k, want := range map[string]string{
						"name": `"apps-node"`, "hostname": `"apps"`, "networkDomain": `{"id":16}`, "plan": `{"id":9401}`, "nodeCount": `3`,
						"sshKeyPair": `null`, "labels": `["MK8S","apps"]`, "visibility": `null`,
						"config": `{"autoScalerEnabled":"on","createUser":true,"customOptions":{"k8s_version":"1.35.3-1.1",` +
							`"podCidr":"10.244.0.0/16","serviceCidr":"10.96.0.0/12"},"maxNodes":"4","minNodes":"1","noAgent":false,` +
							`"nodeCount":3,"resourcePoolId":9201}`,
					} {
						if got := jsonOf(srv[k]); got != want {
							t.Errorf("cluster.server.%s\n got %s\nwant %s", k, got, want)
						}
					}
					// The range goes in the create only: MTN's installer reads it into the cluster's own autoscaler.
					if len(s.scaler) != 0 {
						t.Errorf("platform autoscaler called: %s", jsonOf(s.scaler))
					}
					return nil
				},
			},
			{Config: cfg("", "3", scaling), ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()}}},
			{
				// With autoscaling, the autoscaler owns the count: the plan warns, the apply refuses.
				Config: cfg("", "4", scaling), ExpectError: regexp.MustCompile(`Worker count is set by the autoscaler`)},
			// The range is fixed when the cluster is built: changing or removing it is refused.
			{Config: cfg("", "3", strings.Replace(scaling, "4", "5", 1)), ExpectError: regexp.MustCompile(`Autoscaling is fixed when the cluster is built`)},
			{Config: cfg("", "3", ""), ExpectError: regexp.MustCompile(`Autoscaling is fixed when the cluster is built`)},
			{
				// A description, in place.
				Config: cfg(`  description = "apps cluster"`, "3", scaling), ConfigPlanChecks: inPlace,
				Check: func(*terraform.State) error {
					s.mu.Lock()
					defer s.mu.Unlock()
					if got := jsonOf(s.updated); got != `[{"description":"apps cluster","labels":["MK8S","apps"],"name":"apps"}]` {
						t.Errorf("update: %s", got)
					}
					if len(s.added) != 0 || len(s.removed) != 0 || len(s.scaler) != 0 {
						t.Errorf("added %d, removed %d, autoscaler calls %d; want none", len(s.added), len(s.removed), len(s.scaler))
					}
					return nil
				},
			},
			{Config: cfg(`  description = "apps cluster"`, "3", scaling), ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()}}},
			{
				// What the nodes are built with can't change in place.
				Config: cfg(`  description = "apps cluster"`+"\n  pod_cidr = \"10.200.0.0/16\"", "3", scaling), PlanOnly: true, ExpectNonEmptyPlan: true,
			},
			{
				Config: cfg(`  description = "apps cluster"`, "3", scaling), ResourceName: addr, ImportState: true, ImportStateVerify: true,
				ImportStateVerifyIgnore: []string{"group", "project", "cloud", "resource_name", "hostname", "domain", "registry", "workflow",
					"kubernetes_version", "pod_cidr", "service_cidr", "master", "worker", "autoscaling", "timeouts"},
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

// Masters and workers take their own security groups: the worker block's list goes to the workers
// only, and the project's default group is on both.
func TestCluster_WorkerSecurityGroups(t *testing.T) {
	f, s := clusterFakes(t)
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories(),
		Steps: []resource.TestStep{{
			Config: f.providerConfig() + `
resource "mtncloud_managed_kubernetes_cluster" "k8s" {
  name         = "apps"
  group        = "MTNNG_CLOUD_AZ_1"
  project      = "web-prod-Acme"
  worker_count = 3
  master = {
    plan            = "G2S4"
    data_volume     = [{ size = 50 }]
    networks        = ["app-net"]
    security_groups = ["web"]
  }
  worker = {
    security_groups = ["default"]
  }
}
`,
			Check: func(*terraform.State) error {
				s.mu.Lock()
				defer s.mu.Unlock()
				c := s.posted[0]
				master := jsonOf(c["server"].(map[string]any)["securityGroups"])
				worker := jsonOf(c["worker"].(map[string]any)["securityGroups"])
				if want := `["11111111-1111-4111-8111-111111111111","22222222-2222-4222-8222-222222222222"]`; master != want {
					t.Errorf("master security groups\n got %s\nwant %s (default and web)", master, want)
				}
				if want := `["11111111-1111-4111-8111-111111111111"]`; worker != want {
					t.Errorf("worker security groups\n got %s\nwant %s (default only)", worker, want)
				}
				return nil
			},
		}},
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
			// MTN's layouts don't build a cluster with fewer than three workers (QA clusters 177, 179, 181).
			{Config: cfg("  worker_count = 2", "G2S4"), PlanOnly: true, ExpectError: regexp.MustCompile(`worker_count must be at least 3`)},
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
						"volumes": `[{"datastoreId":"auto","displayOrder":0,"id":-1,"name":"root","rootVolume":true,"size":100,"sizeBytes":107374182400,"storageType":11},` +
							`{"datastoreId":"auto","displayOrder":1,"id":-1,"name":"docker-data","rootVolume":false,"size":100,"sizeBytes":107374182400,"storageType":11}]`,
						"visibility": `null`,
						"config":     `{"createUser":true,"noAgent":false,"nodeCount":1,"resourcePoolId":9201}`,
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

// A cluster that isn't healthy can't be changed: the plan warns, the apply refuses on a fresh read, and
// nothing is sent. A destroy still works, whatever the configuration says.
func TestCluster_ChangesNeedHealthy(t *testing.T) {
	f, s := clusterFakes(t)
	cfg := func(desc string) string {
		return clusterConfig(f, "  worker_count = 3\n  description = \""+desc+"\"", "")
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
			// A destroy plans the configuration first: a change there doesn't stop it (QA t135).
			{Config: cfg("two"), Destroy: true},
		},
		CheckDestroy: func(*terraform.State) error {
			s.mu.Lock()
			defer s.mu.Unlock()
			if _, ok := s.clusters[9601]; ok {
				return fmt.Errorf("cluster 9601 not destroyed")
			}
			return nil
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
			{Config: cfg("3")},
			{
				PreConfig:   func() { s.mu.Lock(); s.failAdd = true; s.mu.Unlock() },
				Config:      cfg("4"),
				ExpectError: regexp.MustCompile(`(?s)Cluster failed.*Failed workers: w-9605.*w-9605\s+failed\s+at\s+kubeadm\s+worker\s+join\s+\(execute\s+script\):\s+bash:\s+line\s+1:\s+null:\s+command\s+not\s+found.*join\s+command`),
			},
			{
				// The failed worker doesn't count: the plan shows the cluster short again.
				Config: cfg("4"), PlanOnly: true, ExpectNonEmptyPlan: true,
			},
			{
				// The apply removes the failed worker and builds a new one.
				PreConfig: func() { s.mu.Lock(); s.failAdd = false; s.mu.Unlock() },
				Config:    cfg("4"),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue("mtncloud_managed_kubernetes_cluster.k8s", tfjsonpath.New("worker_ids"),
						knownvalue.ListExact([]knownvalue.Check{knownvalue.StringExact("9602"), knownvalue.StringExact("9603"),
							knownvalue.StringExact("9604"), knownvalue.StringExact("9606")})),
				},
				Check: func(*terraform.State) error {
					s.mu.Lock()
					defer s.mu.Unlock()
					if got := jsonOf(s.removed); got != "[9605]" {
						t.Errorf("removed %s, want the failed worker", got)
					}
					return nil
				},
			},
			{Config: cfg("4"), ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()}}},
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

// The autoscaler moves the workers; worker_count holds, and the plan stays empty. Turning it off
// afterwards is refused: the range is the cluster's own.
func TestCluster_AutoscalerMovesWorkers(t *testing.T) {
	f, s := clusterFakes(t)
	scaling := "  autoscaling = {\n    min_nodes = 1\n    max_nodes = 5\n  }\n"
	addr := "mtncloud_managed_kubernetes_cluster.k8s"
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories(),
		Steps: []resource.TestStep{
			{Config: clusterConfig(f, "  worker_count = 3", scaling)},
			{
				PreConfig:        func() { s.mu.Lock(); s.addWorkers(9601, 2); s.mu.Unlock() },
				Config:           clusterConfig(f, "  worker_count = 3", scaling),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()}},
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(addr, tfjsonpath.New("worker_count"), knownvalue.Int64Exact(3)),
					statecheck.ExpectKnownValue(addr, tfjsonpath.New("worker_ids"), knownvalue.ListSizeExact(5)),
				},
			},
			{Config: clusterConfig(f, "  worker_count = 3", ""), ExpectError: regexp.MustCompile(`Autoscaling is fixed when the cluster is built`)},
		},
	})
	if len(s.removed) != 0 || len(s.scaler) != 0 {
		t.Errorf("removed %s, autoscaler calls %d; want none", jsonOf(s.removed), len(s.scaler))
	}
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
  input = 3
}
` + clusterConfig(f, "  worker_count = terraform_data.n.output", "")
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories(),
		Steps: []resource.TestStep{
			{
				Config: cfg,
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue("mtncloud_managed_kubernetes_cluster.k8s", tfjsonpath.New("worker_ids"), knownvalue.ListSizeExact(3)),
				},
			},
			{Config: cfg, ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()}}},
		},
	})
	if len(s.posted) != 1 {
		t.Errorf("%d clusters built", len(s.posted))
	}
}

// A cluster built with autoscaling needs nothing more after the build: the create sent the range. A
// cluster deleted out of band is planned again.
func TestCluster_AutoscalingAndDisappears(t *testing.T) {
	f, s := clusterFakes(t)
	cfg := clusterConfig(f, "", "  autoscaling = {\n    min_nodes = 3\n    max_nodes = 6\n  }\n")
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories(),
		Steps: []resource.TestStep{
			{
				Config: cfg,
				Check: func(*terraform.State) error {
					s.mu.Lock()
					defer s.mu.Unlock()
					top := s.posted[0]["config"].(map[string]any)
					if top["autoScalerEnabled"] != "on" || top["minNodes"] != "3" || top["maxNodes"] != "6" {
						t.Errorf("config: %s", jsonOf(top))
					}
					return nil
				},
			},
			{Config: cfg, ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()}}},
			{
				PreConfig: func() { s.mu.Lock(); delete(s.clusters, 9601); s.mu.Unlock() },
				Config:    cfg, PlanOnly: true, ExpectNonEmptyPlan: true,
			},
		},
	})
	if len(s.scaler) != 0 {
		t.Errorf("platform autoscaler called: %s", jsonOf(s.scaler))
	}
}

// Without autoscaling the autoscaler is still sent on, held at worker_count (MTN's order: a cluster
// built with it off never finishes its workers). worker_count then changes by adding and removing
// workers only, never through the platform's autoscaler, which MTN's clusters don't have (QA cluster
// 183, where that call failed and stopped the scaling; owner, 2026-11-11). The root disk is a fixed
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
					if got := jsonOf(srv["volumes"]); got != `[{"datastoreId":"auto","displayOrder":0,"id":-1,"name":"root","rootVolume":true,"size":100,"sizeBytes":107374182400,"storageType":11},`+
						`{"datastoreId":"auto","displayOrder":1,"id":-1,"name":"etcd-data","rootVolume":false,"size":50,"sizeBytes":53687091200,"storageType":11}]` {
						t.Errorf("volumes: %s", got)
					}
					// The worker's disks are the master's, its data disk named as MTN's payload names it.
					if got := jsonOf(c["worker"].(map[string]any)["volumes"]); got != strings.ReplaceAll(jsonOf(srv["volumes"]), "etcd-data", "data") {
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
				Config: cfg("  worker_count = 4"),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(addr, tfjsonpath.New("worker_ids"), knownvalue.ListSizeExact(4)),
				},
				Check: func(*terraform.State) error {
					s.mu.Lock()
					defer s.mu.Unlock()
					if len(s.added) != 1 || len(s.scaler) != 0 {
						t.Errorf("%d adds, %d autoscaler calls; want 1 and none", len(s.added), len(s.scaler))
					}
					return nil
				},
			},
			{Config: cfg("  worker_count = 4"), ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()}}},
			{
				// Back to three: the newest goes. (Left out, the count stays where the cluster is.)
				Config: cfg("  worker_count = 3"),
				Check: func(*terraform.State) error {
					s.mu.Lock()
					defer s.mu.Unlock()
					if got := jsonOf(s.removed); got != "[9605]" || len(s.scaler) != 0 {
						t.Errorf("removed %s, %d autoscaler calls; want [9605] and none", got, len(s.scaler))
					}
					return nil
				},
			},
			{Config: cfg("  worker_count = 3"), ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()}}},
		},
	})
}

// An imported cluster adopts the autoscaling in its configuration: nothing is built, so nothing is refused,
// and the workers its autoscaler made (5, worker_count 3) stay (review, 2026-11-11).
func TestCluster_ImportAdoptsAutoscaling(t *testing.T) {
	f, s := clusterFakes(t)
	s.clusters[9700] = map[string]any{"id": 9700, "name": "apps", "status": "ok", "layout": map[string]any{"id": 315},
		"type": map[string]any{"code": "kubernetes-cluster", "name": "Kubernetes Cluster"}}
	s.addWorkers(9700, 5)
	cfg := clusterConfig(f, "  worker_count = 3", "  autoscaling = {\n    min_nodes = 3\n    max_nodes = 6\n  }\n")
	addr := "mtncloud_managed_kubernetes_cluster.k8s"
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories(),
		Steps: []resource.TestStep{
			{Config: cfg, ResourceName: addr, ImportState: true, ImportStateId: "9700", ImportStatePersist: true},
			{Config: cfg},
			{Config: cfg, ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()}}},
		},
	})
	if len(s.posted) != 0 || len(s.added) != 0 || len(s.removed) != 0 || len(s.scaler) != 0 {
		t.Errorf("built %d, added %d, removed %d, autoscaler calls %d; want none", len(s.posted), len(s.added), len(s.removed), len(s.scaler))
	}
}

// A 0.11.0 cluster with fewer than three workers still plans, changes in place and destroys; only a
// count changed below three is refused.
func TestCluster_FewWorkersKept(t *testing.T) {
	f, s := clusterFakes(t)
	s.clusters[9700] = map[string]any{"id": 9700, "name": "apps", "status": "ok", "layout": map[string]any{"id": 315},
		"type": map[string]any{"code": "kubernetes-cluster", "name": "Kubernetes Cluster"}}
	s.addWorkers(9700, 2)
	cfg := func(top string) string { return clusterConfig(f, top, "") }
	two := func(n string) string { return "  worker_count = " + n + "\n  description = \"two\"" }
	addr := "mtncloud_managed_kubernetes_cluster.k8s"
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories(),
		Steps: []resource.TestStep{
			{Config: cfg("  worker_count = 2"), ResourceName: addr, ImportState: true, ImportStateId: "9700", ImportStatePersist: true},
			{Config: cfg("  worker_count = 2")},
			{Config: cfg(two("2")), ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
				plancheck.ExpectResourceAction(addr, plancheck.ResourceActionUpdate)}}},
			{Config: cfg(two("1")), ExpectError: regexp.MustCompile(`worker_count must be at least 3`)},
			{Config: cfg(two("2")), Destroy: true},
		},
	})
	if len(s.added) != 0 || len(s.removed) != 0 {
		t.Errorf("added %d, removed %d; want none", len(s.added), len(s.removed))
	}
}

// An unhealthy cluster can still be rebuilt: a change that builds a new cluster plans a replacement even
// with an in-place change beside it (review, 2026-11-03).
func TestCluster_UnhealthyCanBeReplaced(t *testing.T) {
	f, s := clusterFakes(t)
	cfg := func(extra string) string {
		return clusterConfig(f, "  worker_count = 3\n  description = \"two\"\n"+extra, "")
	}
	addr := "mtncloud_managed_kubernetes_cluster.k8s"
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories(),
		Steps: []resource.TestStep{
			{Config: clusterConfig(f, "  worker_count = 3", "")},
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
	s.addWorkers(9700, 3)
	cfg := func(name string) string {
		return strings.Replace(clusterConfig(f, "  worker_count = 3", ""), `name    = "apps"`, `name    = "`+name+`"`, 1)
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

// A destroy plans the configuration first: a change the apply would refuse doesn't stop it (QA t135).
func TestCluster_DestroyIgnoresRefusedChange(t *testing.T) {
	f, s := clusterFakes(t)
	cfg := func(n string) string {
		return clusterConfig(f, "  worker_count = "+n+"\n  autoscaling = {\n    min_nodes = 1\n    max_nodes = 4\n  }", "")
	}
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories(),
		Steps: []resource.TestStep{
			{Config: cfg("3")},
			{Config: cfg("4"), Destroy: true},
		},
		CheckDestroy: func(*terraform.State) error {
			s.mu.Lock()
			defer s.mu.Unlock()
			if _, ok := s.clusters[9601]; ok {
				return fmt.Errorf("cluster 9601 not destroyed")
			}
			return nil
		},
	})
	if len(s.added) != 0 {
		t.Errorf("workers added: %s", jsonOf(s.added))
	}
}

// MTN Cloud's Kubernetes sync defect (owner, 2026-11-11): every node is built and the cluster still says
// "error", "No kube masters found". The apply succeeds, the next plan is empty, and it changes in place.
// The node's disks go on the datastore chosen, and a worker count left out is 3.
func TestCluster_SyncDefectIsBuilt(t *testing.T) {
	f, s := clusterFakes(t)
	s.syncDefect = true
	cfg := func(desc string) string {
		return f.providerConfig() + `
resource "mtncloud_managed_kubernetes_cluster" "k8s" {
  name        = "apps"
  group       = "MTNNG_CLOUD_AZ_1"
  project     = "web-prod-Acme"
  description = "` + desc + `"
  master = {
    plan           = "G2S4"
    disk_datastore = "ENCR_AT_REST"
    data_volume    = [{ size = 50 }, { size = 20, datastore = "__DEFAULT__" }]
    networks       = ["app-net"]
  }
}
`
	}
	addr := "mtncloud_managed_kubernetes_cluster.k8s"
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories(),
		Steps: []resource.TestStep{
			{
				Config: cfg("one"),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(addr, tfjsonpath.New("status"), knownvalue.StringExact("error")),
					statecheck.ExpectKnownValue(addr, tfjsonpath.New("worker_count"), knownvalue.Int64Exact(3)),
					statecheck.ExpectKnownValue(addr, tfjsonpath.New("worker_ids"), knownvalue.ListSizeExact(3)),
				},
				Check: func(*terraform.State) error {
					s.mu.Lock()
					defer s.mu.Unlock()
					srv := s.posted[0]["server"].(map[string]any)
					vols := srv["volumes"].([]any)
					// A data disk without its own datastore takes disk_datastore; one with its own keeps it.
					if len(vols) != 3 || vols[0].(map[string]any)["datastoreId"] != "414" || vols[1].(map[string]any)["datastoreId"] != "414" ||
						vols[2].(map[string]any)["datastoreId"] != "2" {
						t.Errorf("volumes: %s", jsonOf(vols))
					}
					if jsonOf(srv["nodeCount"]) != "3" {
						t.Errorf("server.nodeCount %s, want 3", jsonOf(srv["nodeCount"]))
					}
					// The workers take the master's disk datastore.
					wv := s.posted[0]["worker"].(map[string]any)["volumes"].([]any)
					if len(wv) != 3 || wv[0].(map[string]any)["datastoreId"] != "414" || wv[1].(map[string]any)["datastoreId"] != "414" ||
						wv[2].(map[string]any)["datastoreId"] != "2" {
						t.Errorf("worker volumes: %s", jsonOf(wv))
					}
					return nil
				},
			},
			{Config: cfg("one"), ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()}}},
			{
				Config: cfg("two"),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction(addr, plancheck.ResourceActionUpdate)}},
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(addr, tfjsonpath.New("description"), knownvalue.StringExact("two"))},
			},
		},
	})
}

// A disk_datastore the project doesn't have fails the plan; it never falls back to the project's default.
func TestCluster_UnknownDiskDatastore(t *testing.T) {
	f, _ := clusterFakes(t)
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories(),
		Steps: []resource.TestStep{{
			Config: f.providerConfig() + `
resource "mtncloud_docker_cluster" "d" {
  name    = "apps"
  group   = "MTNNG_CLOUD_AZ_1"
  project = "web-prod-Acme"
  host = {
    plan           = "G2S4"
    disk_datastore = "NO_SUCH_STORE"
    data_volume    = [{ size = 50 }]
    networks       = ["app-net"]
  }
}
`,
			PlanOnly:    true,
			ExpectError: regexp.MustCompile(`no datastore named "NO_SUCH_STORE"`),
		}},
	})
}

// A Docker cluster with host_count left out gets 3 hosts (owner, 2026-11-11), and plans clean after.
func TestDockerCluster_DefaultCount(t *testing.T) {
	f, s := clusterFakes(t)
	cfg := f.providerConfig() + `
resource "mtncloud_docker_cluster" "docker" {
  name    = "builds"
  group   = "MTNNG_CLOUD_AZ_1"
  project = "web-prod-Acme"
  host = {
    plan        = "G2S4"
    data_volume = [{ size = 100 }]
    networks    = ["app-net"]
  }
}
`
	addr := "mtncloud_docker_cluster.docker"
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories(),
		Steps: []resource.TestStep{
			{
				Config: cfg,
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(addr, tfjsonpath.New("host_count"), knownvalue.Int64Exact(3)),
					statecheck.ExpectKnownValue(addr, tfjsonpath.New("host_ids"), knownvalue.ListSizeExact(3)),
				},
				Check: func(*terraform.State) error {
					s.mu.Lock()
					defer s.mu.Unlock()
					if got := jsonOf(s.posted[0]["server"].(map[string]any)["nodeCount"]); got != "3" {
						t.Errorf("server.nodeCount %s, want 3", got)
					}
					return nil
				},
			},
			{Config: cfg, ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()}}},
		},
	})
}
