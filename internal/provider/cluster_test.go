package provider

import (
	"encoding/json"
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
}

func newClusterStore() *clusterStore {
	return &clusterStore{next: 9600, clusters: map[int64]map[string]any{}, workers: map[int64][]int64{}}
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
			typ := map[string]any{"code": c["type"]}
			s.clusters[id] = map[string]any{"id": id, "name": c["name"], "description": c["description"], "labels": c["labels"],
				"status": "ok", "serviceUrl": "https://10.0.0.10:6443", "layout": map[string]any{"id": layout}, "type": typ}
			srv := c["server"].(map[string]any)
			s.addWorkers(id, int(srv["nodeCount"].(float64)))
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
			list = append(list, map[string]any{"id": wid, "name": "w-" + strconv.FormatInt(wid, 10), "status": "provisioned"})
		}
		reply(map[string]any{"workers": list})
	case parts[1] == "servers" && r.Method == http.MethodPost:
		b := body("server")
		s.added = append(s.added, b)
		s.addWorkers(id, int(b["nodeCount"].(float64)))
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
resource "mtncloud_cluster" "k8s" {
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
    root_volume_size = 40
    data_volume      = [{ size = 50, datastore = "ENCR_AT_REST" }]
    networks         = ["app-net"]
    security_groups  = ["web"]
    floating_ip_pool = "public-network-01"
  }
  worker = {
    plan = "G4S16"
  }
` + scaling + `}
`
	}
	scaling := "  autoscaling = {\n    min_nodes = 1\n    max_nodes = 4\n  }\n"
	addr := "mtncloud_cluster.k8s"
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
					check("type", knownvalue.StringExact("kubernetes")),
					check("layout", knownvalue.StringExact("MTN Kubernetes Single-Master")),
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
					for k, want := range map[string]string{
						"layout": `{"id":315}`, "group": `{"id":9101}`, "cloud": `{"id":4}`, "type": `"kubernetes-cluster"`,
						"taskSetId": `61`, "labels": `["apps"]`,
						"worker": `{"config":{"osExternalNetworkId":"pool-44","resourcePoolId":9201},` +
							`"networkInterfaces":[{"network":{"id":"network-9350"},"primaryInterface":true}],"plan":{"id":9402},` +
							`"securityGroups":[{"id":9301},{"id":9302}],"volumes":[{"name":"root","rootVolume":true,"size":40},` +
							`{"name":"data-1","rootVolume":false,"size":50,"storageType":414}]}`,
					} {
						if got := jsonOf(c[k]); got != want {
							t.Errorf("cluster.%s\n got %s\nwant %s", k, got, want)
						}
					}
					for k, want := range map[string]string{
						"name": `"apps-node"`, "hostname": `"apps"`, "networkDomain": `{"id":16}`, "nodeCount": `2`, "plan": `{"id":9401}`,
						"config": `{"customOptions":{"k8s_version":"1.35.3-1.1","podCidr":"10.244.0.0/16","serviceCidr":"10.96.0.0/12"},` +
							`"defaultRepoAccount":9501,"nodeCount":2,"osExternalNetworkId":"pool-44","podCidr":"10.244.0.0/16",` +
							`"resourcePoolId":9201,"serviceCidr":"10.96.0.0/12"}`,
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
				// One more worker, with the worker settings, and a description: both in place.
				Config: cfg(`  description = "apps cluster"`, "3", scaling), ConfigPlanChecks: inPlace,
				ConfigStateChecks: []statecheck.StateCheck{check("worker_ids", knownvalue.ListSizeExact(3))},
				Check: func(*terraform.State) error {
					s.mu.Lock()
					defer s.mu.Unlock()
					a := s.added[0]
					if jsonOf(a["nodeCount"]) != "1" || jsonOf(a["plan"]) != `{"id":9402}` || jsonOf(a["cloud"]) != `{"id":4}` {
						t.Errorf("worker add: %s", jsonOf(a))
					}
					if got := jsonOf(s.updated[0]); got != `{"description":"apps cluster","labels":["apps"],"name":"apps"}` {
						t.Errorf("update: %s", got)
					}
					return nil
				},
			},
			{
				// Down to one: the newest two go. The autoscaler is turned off.
				Config: cfg(`  description = "apps cluster"`, "1", ""), ConfigPlanChecks: inPlace,
				Check: func(*terraform.State) error {
					s.mu.Lock()
					defer s.mu.Unlock()
					if got := jsonOf(s.removed); got != "[9604,9603]" {
						t.Errorf("removed %s, want the newest two", got)
					}
					if got := jsonOf(s.scaler[len(s.scaler)-1]); got != `{"autoScalerEnabled":false}` {
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
resource "mtncloud_cluster" "k8s" {
  name    = "apps"
  group   = "MTNNG_CLOUD_AZ_1"
  project = "web-prod-Acme"
` + extra + `
  master = {
    plan             = "` + masterPlan + `"
    root_volume_size = 40
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
				ExpectError: regexp.MustCompile(`no cluster layout(.|\n)*MTN Kubernetes HA \(3\)`)},
			{Config: cfg("  type = \"docker\"\n  pod_cidr = \"10.244.0.0/16\"", "G2S4"), PlanOnly: true,
				ExpectError: regexp.MustCompile(`pod_cidr is for Kubernetes clusters only`)},
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

// A Docker cluster: MTN's layout, no Kubernetes inputs, the hosts from master.
func TestCluster_Docker(t *testing.T) {
	f, s := clusterFakes(t)
	cfg := f.providerConfig() + `
resource "mtncloud_cluster" "docker" {
  name    = "builds"
  type    = "docker"
  group   = "MTNNG_CLOUD_AZ_1"
  project = "web-prod-Acme"
  master = {
    plan             = "G2S4"
    root_volume_size = 40
    data_volume      = [{ size = 100 }]
    networks         = ["app-net"]
  }
}
`
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories(),
		Steps: []resource.TestStep{
			{
				Config: cfg,
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue("mtncloud_cluster.docker", tfjsonpath.New("layout"), knownvalue.StringExact("MTN Docker Cluster")),
					statecheck.ExpectKnownValue("mtncloud_cluster.docker", tfjsonpath.New("worker_count"), knownvalue.Int64Exact(1)),
					statecheck.ExpectKnownValue("mtncloud_cluster.docker", tfjsonpath.New("pod_cidr"), knownvalue.Null()),
				},
				Check: func(*terraform.State) error {
					s.mu.Lock()
					defer s.mu.Unlock()
					c := s.posted[0]
					if jsonOf(c["type"]) != `"docker-cluster"` || jsonOf(c["layout"]) != `{"id":311}` || c["worker"] != nil {
						t.Errorf("docker cluster: %s", jsonOf(c))
					}
					if cfg := c["server"].(map[string]any)["config"].(map[string]any); cfg["customOptions"] != nil || cfg["podCidr"] != nil {
						t.Errorf("Kubernetes inputs sent for Docker: %s", jsonOf(cfg))
					}
					return nil
				},
			},
			{Config: cfg, ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()}}},
		},
	})
}

func clusterConfig(f *fakeAPI, top, scaling string) string {
	return f.providerConfig() + `
resource "mtncloud_cluster" "k8s" {
  name    = "apps"
  group   = "MTNNG_CLOUD_AZ_1"
  project = "web-prod-Acme"
` + top + `
  master = {
    plan             = "G2S4"
    root_volume_size = 40
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
	addr := "mtncloud_cluster.k8s"
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

// What you type is kept: a version given as its value, a layout in other casing.
func TestCluster_KeepsWhatYouTyped(t *testing.T) {
	f, s := clusterFakes(t)
	cfg := clusterConfig(f, "  layout = \"mtn kubernetes single-master\"\n  kubernetes_version = \"1.35.3-1.1\"", "")
	addr := "mtncloud_cluster.k8s"
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories(),
		Steps: []resource.TestStep{
			{
				Config: cfg,
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(addr, tfjsonpath.New("layout"), knownvalue.StringExact("mtn kubernetes single-master")),
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
					statecheck.ExpectKnownValue("mtncloud_cluster.k8s", tfjsonpath.New("worker_ids"), knownvalue.ListSizeExact(2)),
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
	addr := "mtncloud_cluster.k8s"
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
