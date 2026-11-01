package provider

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/knownvalue"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/statecheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"
)

// depFake is MTN Cloud's deployments, their versions and files, and instance 4700's deploys, as live
// QA saw them (2026-11-01): PUT changes what it is sent, deleting a deployment deletes its versions,
// an upload of the same name overwrites, and a new deploy archives the one it replaces. A deploy is
// marked committed even when its run (a "deploy" process in the instance's history) fails.
type depFake struct {
	mu       sync.Mutex
	deps     map[int64]map[string]any
	versions map[int64]map[string]any
	files    map[int64]map[string]int // version -> name -> size
	deploys  []map[string]any         // instance 4700's, oldest first
	next     int64
	uploads  int
	failNext bool // the next deploy's run fails
	hideRuns bool // the history list leaves out deploy runs, as when they have scrolled out of it
}

func newDepFake(f *fakeAPI) *depFake {
	d := &depFake{deps: map[int64]map[string]any{}, versions: map[int64]map[string]any{}, files: map[int64]map[string]int{}, next: 30}
	f.mu.Lock()
	f.extra, f.extraFor = d, func(p string) bool {
		return strings.HasPrefix(p, "/api/deployments") || strings.HasPrefix(p, "/api/instances/4700/deploys") || strings.HasPrefix(p, "/api/processes")
	}
	f.mu.Unlock()
	return d
}

func (d *depFake) id() int64 { d.next++; return d.next }

func (d *depFake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	d.mu.Lock()
	defer d.mu.Unlock()
	write := func(v any) { b, _ := json.Marshal(v); _, _ = w.Write(b) }
	notFound := func() {
		w.WriteHeader(http.StatusNotFound)
		write(map[string]any{"success": false, "msg": "not found"})
	}
	raw, _ := io.ReadAll(r.Body)
	var in map[string]map[string]any
	_ = json.Unmarshal(raw, &in)
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/") // api deployments {id} versions {id} files {name}
	num := func(i int) int64 {
		if i >= len(parts) {
			return 0
		}
		n, _ := strconv.ParseInt(parts[i], 10, 64)
		return n
	}
	if parts[1] == "processes" { // instance 4700's history, newest first: a run per deploy, and a workflow run
		list := []any{map[string]any{"id": 4999, "processType": map[string]any{"code": "instanceWorkflow"}, "status": "failed",
			"startDate": deployTime(d.next + 5).Format(time.RFC3339)}}
		for i := len(d.deploys) - 1; i >= 0; i-- {
			x := d.deploys[i]
			if x["run"] == "" {
				continue
			}
			msg := ""
			if x["run"] == "failed" {
				msg = "Failed to build deploy config. This Container Node Type may be missing a deploy folder configuration."
			}
			list = append(list, map[string]any{"id": 5000 + x["id"].(int64), "processType": map[string]any{"code": "deploy"}, "status": x["run"],
				"message": msg, "startDate": deployTime(x["id"].(int64)).Add(4 * time.Second).Format(time.RFC3339)})
		}
		if len(parts) == 3 { // one process, by ID: always there
			for _, p := range list {
				if fmt.Sprint(p.(map[string]any)["id"]) == parts[2] {
					write(map[string]any{"process": p})
					return
				}
			}
			notFound()
			return
		}
		if r.URL.Query().Get("offset") != "0" || d.hideRuns {
			list = []any{} // one page holds it all
		}
		write(map[string]any{"processes": list})
		return
	}
	if parts[1] == "instances" { // /api/instances/4700/deploys
		switch r.Method {
		case http.MethodGet:
			list := []any{}
			for i := len(d.deploys) - 1; i >= 0; i-- {
				x := d.deploys[i]
				if x["status"] == "deploying" { // settles on the first read, archiving the one it replaces
					d.commit(x)
					x["run"] = "complete"
					if d.failNext {
						x["run"], d.failNext = "failed", false
					}
				}
				list = append(list, x)
			}
			write(map[string]any{"appDeploys": list})
		case http.MethodPost:
			ad := in["appDeploy"]
			vid := int64(ad["versionId"].(float64))
			v, ok := d.versions[vid]
			if !ok {
				w.WriteHeader(http.StatusBadRequest)
				write(map[string]any{"success": false, "msg": "Deployment Version not found"})
				return
			}
			id := d.id()
			x := map[string]any{"id": id, "instanceId": 4700, "deploymentVersionId": vid, "status": "deploying", "config": ad["config"],
				"dateCreated": deployTime(id).Format(time.RFC3339), "run": "",
				"deployment":        map[string]any{"id": v["deploymentId"], "name": "x"},
				"deploymentVersion": map[string]any{"id": vid, "userVersion": v["userVersion"]}}
			d.deploys = append(d.deploys, x)
			write(map[string]any{"appDeploy": x})
		}
		return
	}
	depID, verID := num(2), num(4)
	switch {
	case len(parts) == 2 && r.Method == http.MethodGet:
		list := []any{}
		for _, x := range d.deps {
			if strings.EqualFold(x["name"].(string), r.URL.Query().Get("name")) {
				list = append(list, x)
			}
		}
		write(map[string]any{"deployments": list})
	case len(parts) == 2 && r.Method == http.MethodPost:
		x := map[string]any{"id": d.id(), "name": in["deployment"]["name"], "description": in["deployment"]["description"]}
		if x["description"] == "" {
			x["description"] = nil
		}
		d.deps[x["id"].(int64)] = x
		write(map[string]any{"success": true, "deployment": x})
	case len(parts) == 3:
		x, ok := d.deps[depID]
		if !ok {
			notFound()
			return
		}
		switch r.Method {
		case http.MethodGet:
			write(map[string]any{"deployment": x})
		case http.MethodPut:
			for k, v := range in["deployment"] {
				if v == "" {
					v = nil
				}
				x[k] = v
			}
			write(map[string]any{"success": true, "deployment": x})
		case http.MethodDelete:
			delete(d.deps, depID)
			for id, v := range d.versions {
				if v["deploymentId"] == depID {
					delete(d.versions, id)
					delete(d.files, id)
				}
			}
			write(map[string]any{"success": true})
		}
	case len(parts) == 4: // versions
		if _, ok := d.deps[depID]; !ok {
			notFound()
			return
		}
		if r.Method == http.MethodPost {
			x := map[string]any{"id": d.id(), "deploymentId": depID, "status": "created"}
			d.setVersion(x, in["version"])
			d.versions[x["id"].(int64)] = x
			write(map[string]any{"success": true, "version": x})
			return
		}
		list := []any{}
		for _, v := range d.versions {
			if v["deploymentId"] == depID {
				list = append(list, v)
			}
		}
		write(map[string]any{"versions": list})
	case len(parts) == 5:
		v, ok := d.versions[verID]
		if !ok || v["deploymentId"] != depID {
			notFound()
			return
		}
		switch r.Method {
		case http.MethodGet:
			write(map[string]any{"version": v})
		case http.MethodPut:
			d.setVersion(v, in["version"])
			write(map[string]any{"success": true, "version": v})
		case http.MethodDelete:
			delete(d.versions, verID)
			delete(d.files, verID)
			write(map[string]any{"success": true})
		}
	case parts[5] == "files":
		if _, ok := d.versions[verID]; !ok {
			notFound()
			return
		}
		if d.files[verID] == nil {
			d.files[verID] = map[string]int{}
		}
		switch r.Method {
		case http.MethodGet:
			list := []any{}
			for n, size := range d.files[verID] {
				list = append(list, map[string]any{"name": n, "directory": false, "contentLength": size})
			}
			write(map[string]any{"files": list})
		case http.MethodPost:
			d.uploads++
			d.files[verID][r.URL.Query().Get("filename")] = len(raw)
			write(map[string]any{"success": true})
		case http.MethodDelete:
			delete(d.files[verID], parts[6])
			write(map[string]any{"success": true})
		}
	}
}

// deployTime is when the fake made deploy id: whole seconds, as MTN Cloud gives them.
func deployTime(id int64) time.Time {
	return time.Date(2026, 11, 1, 7, 0, 0, 0, time.UTC).Add(time.Duration(id) * 10 * time.Second)
}

// commit makes x the deploy live on the instance.
func (d *depFake) commit(x map[string]any) {
	for _, y := range d.deploys {
		if y["id"] != x["id"] && y["status"] == "committed" {
			y["status"] = "archived"
		}
	}
	x["status"] = "committed"
}

// add appends a deploy made outside Terraform, of the newest deploy's version or, with other, of
// another version of the deployment. run is its process's status ("" for none: a staged deploy).
func (d *depFake) add(status, run string, other bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	last := d.deploys[len(d.deploys)-1]
	vid, ver := last["deploymentVersionId"], last["deploymentVersion"]
	if other {
		for id, v := range d.versions {
			if id != vid && v["deploymentId"] == last["deployment"].(map[string]any)["id"] {
				vid, ver = id, map[string]any{"id": id, "userVersion": v["userVersion"]}
				break
			}
		}
	}
	id := d.id()
	x := map[string]any{"id": id, "instanceId": 4700, "deploymentVersionId": vid, "status": status, "run": run,
		"dateCreated": deployTime(id).Format(time.RFC3339), "deployment": last["deployment"], "deploymentVersion": ver}
	d.deploys = append(d.deploys, x)
	if status == "committed" {
		d.commit(x)
	}
}

func (d *depFake) setVersion(x, in map[string]any) {
	for k, v := range in {
		if v == "" {
			v = nil
		}
		x[k] = v
	}
	x["userVersion"] = x["version"]
}

func TestDeployment(t *testing.T) {
	f := newFakeAPI(t)
	d := newDepFake(f)
	cfg := func(depDesc, fetchURL, deployVersion, fileContent string) string {
		desc := ""
		if depDesc != "" {
			desc = fmt.Sprintf("  description = %q\n", depDesc)
		}
		return f.providerConfig() + fmt.Sprintf(`
resource "mtncloud_deployment" "web" {
  name = "web"
%s}
resource "mtncloud_deployment_version" "v1" {
  deployment_id = mtncloud_deployment.web.id
  version       = "1.0"
  type          = "fetch"
  fetch_url     = %q
}
resource "mtncloud_deployment_version" "v2" {
  deployment_id = mtncloud_deployment.web.id
  version       = "2.0"
  type          = "file"
}
resource "mtncloud_deployment_file" "index" {
  deployment_id = mtncloud_deployment.web.id
  version_id    = mtncloud_deployment_version.v2.id
  name          = "index.html"
  content       = %q
}
resource "mtncloud_instance_deploy" "web" {
  instance_id   = "4700"
  deployment_id = mtncloud_deployment.web.id
  version_id    = mtncloud_deployment_version.%s.id
  depends_on    = [mtncloud_deployment_file.index]
}
`, desc, fetchURL, fileContent, deployVersion)
	}
	const dep, v1, file, inst = "mtncloud_deployment.web", "mtncloud_deployment_version.v1", "mtncloud_deployment_file.index", "mtncloud_instance_deploy.web"
	empty := resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()}}
	update := func(addr string) resource.ConfigPlanChecks {
		return resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(addr, plancheck.ResourceActionUpdate)}}
	}
	deploys := func(want ...string) func(*terraform.State) error {
		return func(*terraform.State) error {
			d.mu.Lock()
			defer d.mu.Unlock()
			got := []string{}
			for _, x := range d.deploys {
				got = append(got, fmt.Sprint(x["status"]))
			}
			if strings.Join(got, ",") != strings.Join(want, ",") {
				return fmt.Errorf("deploys %v, want %v", got, want)
			}
			return nil
		}
	}
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories(),
		Steps: []resource.TestStep{
			// Plan-time checks.
			{Config: f.providerConfig() + `
resource "mtncloud_deployment_version" "x" {
  deployment_id = "1"
  version       = "1"
  type          = "fetch"
}`, ExpectError: regexp.MustCompile(`A fetch version needs fetch_url`)},
			{Config: f.providerConfig() + `
resource "mtncloud_deployment_version" "x" {
  deployment_id = "1"
  version       = "1"
  type          = "file"
  git_url       = "https://example.com/a.git"
}`, ExpectError: regexp.MustCompile(`git_url is only used with type = "git"`)},
			{Config: f.providerConfig() + `
resource "mtncloud_deployment_file" "x" {
  deployment_id = "1"
  version_id    = "2"
  name          = "web/index.html"
  content       = "x"
}`, ExpectError: regexp.MustCompile(`must be a file name with no folder`)},
			// Create all, deploying v1, then a clean plan.
			{Config: cfg("site", "https://example.com/a", "v1", "hello"), Check: deploys("committed"),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(inst, tfjsonpath.New("status"), knownvalue.StringExact("committed")),
					statecheck.ExpectKnownValue(inst, tfjsonpath.New("version"), knownvalue.StringExact("1.0")),
					statecheck.ExpectKnownValue(file, tfjsonpath.New("size_bytes"), knownvalue.Int64Exact(5)),
					statecheck.ExpectKnownValue(v1, tfjsonpath.New("status"), knownvalue.StringExact("created")),
				}},
			{Config: cfg("site", "https://example.com/a", "v1", "hello"), ConfigPlanChecks: empty},
			// In place: the description cleared and the fetch URL changed; nothing deployed again.
			{Config: cfg("", "https://example.org/b", "v1", "hello"), ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
				plancheck.ExpectResourceAction(dep, plancheck.ResourceActionUpdate), plancheck.ExpectResourceAction(v1, plancheck.ResourceActionUpdate),
				plancheck.ExpectResourceAction(inst, plancheck.ResourceActionNoop),
			}}, ConfigStateChecks: []statecheck.StateCheck{
				statecheck.ExpectKnownValue(dep, tfjsonpath.New("description"), knownvalue.Null()),
				statecheck.ExpectKnownValue(v1, tfjsonpath.New("fetch_url"), knownvalue.StringExact("https://example.org/b")),
			}, Check: deploys("committed")},
			{Config: cfg("", "https://example.org/b", "v1", "hello"), ConfigPlanChecks: empty},
			// Another version deploys in place; the old deploy is archived.
			{Config: cfg("", "https://example.org/b", "v2", "hello"), ConfigPlanChecks: update(inst), Check: deploys("archived", "committed"),
				ConfigStateChecks: []statecheck.StateCheck{statecheck.ExpectKnownValue(inst, tfjsonpath.New("version"), knownvalue.StringExact("2.0"))}},
			{Config: cfg("", "https://example.org/b", "v2", "hello"), ConfigPlanChecks: empty},
			// New content uploads the file again in place.
			{Config: cfg("", "https://example.org/b", "v2", "hello, world"), ConfigPlanChecks: update(file),
				Check: func(*terraform.State) error {
					d.mu.Lock()
					defer d.mu.Unlock()
					if d.uploads != 2 {
						return fmt.Errorf("%d uploads, want 2", d.uploads)
					}
					return nil
				}},
			{Config: cfg("", "https://example.org/b", "v2", "hello, world"), ConfigPlanChecks: empty},
			// Changed outside Terraform: the file's size and the version deployed. The plan puts both back.
			{PreConfig: func() {
				d.mu.Lock()
				for v := range d.files {
					for n := range d.files[v] {
						d.files[v][n] = 1
					}
				}
				d.mu.Unlock()
				d.add("committed", "complete", true)
			}, Config: cfg("", "https://example.org/b", "v2", "hello, world"), ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
				plancheck.ExpectResourceAction(file, plancheck.ResourceActionUpdate), plancheck.ExpectResourceAction(inst, plancheck.ResourceActionUpdate),
			}}, Check: deploys("archived", "archived", "archived", "committed")},
			{Config: cfg("", "https://example.org/b", "v2", "hello, world"), ConfigPlanChecks: empty},
			// A deploy whose run fails: MTN Cloud still marks it committed, the error gives the run's reason,
			// and the next apply deploys again.
			{PreConfig: func() { d.mu.Lock(); d.failNext = true; d.mu.Unlock() }, Config: cfg("", "https://example.org/b", "v1", "hello, world"),
				ExpectError: regexp.MustCompile(`failed\s+on\s+the\s+instance:\s+Failed\s+to\s+build\s+deploy\s+config`)},
			{Config: cfg("", "https://example.org/b", "v1", "hello, world"), ConfigPlanChecks: update(inst)},
			{Config: cfg("", "https://example.org/b", "v1", "hello, world"), ConfigPlanChecks: empty},
			// A deploy of another version staged outside Terraform isn't live: nothing changes.
			{PreConfig: func() { d.add("staged", "", true) }, Config: cfg("", "https://example.org/b", "v1", "hello, world"), ConfigPlanChecks: empty},
			// Someone redeploys the same version and its run fails: the plan deploys again.
			{PreConfig: func() { d.add("committed", "failed", false) }, Config: cfg("", "https://example.org/b", "v1", "hello, world"),
				ConfigPlanChecks: update(inst), ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(inst, tfjsonpath.New("status"), knownvalue.StringExact("committed")),
				}},
			{Config: cfg("", "https://example.org/b", "v1", "hello, world"), ConfigPlanChecks: empty},
			// The live deploy's run, read by its ID, failed though the history list no longer shows it: the
			// plan deploys again. With the run complete again, nothing changes.
			{PreConfig: func() {
				d.mu.Lock()
				defer d.mu.Unlock()
				d.hideRuns = true
				d.deploys[len(d.deploys)-1]["run"] = "failed"
			}, Config: cfg("", "https://example.org/b", "v1", "hello, world"), PlanOnly: true, ExpectNonEmptyPlan: true},
			{PreConfig: func() {
				d.mu.Lock()
				defer d.mu.Unlock()
				d.hideRuns = false
				d.deploys[len(d.deploys)-1]["run"] = "complete"
			}, Config: cfg("", "https://example.org/b", "v1", "hello, world"), ConfigPlanChecks: empty},
			// Imports.
			{ResourceName: dep, ImportState: true, ImportStateVerify: true},
			{ResourceName: v1, ImportState: true, ImportStateVerify: true, ImportStateIdFunc: func(s *terraform.State) (string, error) {
				a := s.RootModule().Resources[v1].Primary.Attributes
				return a["deployment_id"] + "/" + a["id"], nil
			}},
			{ResourceName: file, ImportState: true, ImportStateVerify: true, ImportStateVerifyIgnore: []string{"content", "content_sha256"}},
			{ResourceName: inst, ImportState: true, ImportStateVerify: true, ImportStateVerifyIgnore: []string{"timeouts"}},
			{ResourceName: dep, ImportState: true, ImportStateId: "web", ExpectError: regexp.MustCompile(`Expected the deployment's ID`)},
		},
		CheckDestroy: func(*terraform.State) error {
			d.mu.Lock()
			defer d.mu.Unlock()
			if len(d.deps) != 0 || len(d.versions) != 0 {
				return fmt.Errorf("left %d deployments, %d versions", len(d.deps), len(d.versions))
			}
			if d.deploys[len(d.deploys)-1]["status"] != "committed" {
				return fmt.Errorf("destroy touched the live deploy: %v", d.deploys[len(d.deploys)-1])
			}
			return nil
		},
	})
}

// A deployment, a version or a file that already exists is never taken over: the create names it
// for import.
func TestDeployment_Existing(t *testing.T) {
	f := newFakeAPI(t)
	d := newDepFake(f)
	d.deps[5] = map[string]any{"id": int64(5), "name": "web", "description": nil}
	d.versions[6] = map[string]any{"id": int64(6), "deploymentId": int64(5), "version": "1.0", "userVersion": "1.0", "deployType": "file", "status": "created"}
	d.files[6] = map[string]int{"index.html": 3}
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories(),
		Steps: []resource.TestStep{
			{Config: f.providerConfig() + `
resource "mtncloud_deployment" "web" {
  name = "WEB"
}`, ExpectError: regexp.MustCompile(`(?s)Deployment already exists.*terraform import <address> 5`)},
			{Config: f.providerConfig() + `
resource "mtncloud_deployment_version" "v1" {
  deployment_id = "5"
  version       = "1.0"
  type          = "file"
}`, ExpectError: regexp.MustCompile(`(?s)Version already exists.*5/6`)},
			{Config: f.providerConfig() + `
resource "mtncloud_deployment_file" "index" {
  deployment_id = "5"
  version_id    = "6"
  name          = "index.html"
  content       = "new"
}`, ExpectError: regexp.MustCompile(`(?s)File already exists.*5/6/index.html`)},
		},
	})
}
