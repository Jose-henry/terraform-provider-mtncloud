package legacy

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	sdk "github.com/gomorpheus/morpheus-go-sdk"
	"github.com/hashicorp/terraform-plugin-sdk/v2/diag"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
	"github.com/hashicorp/terraform-plugin-sdk/v2/terraform"
)

// Tests for the fixes from the tester's report of 2026-11-17 (fixes 14 to 19).

// call is one request the fake API received.
type call struct {
	Method, Path, Query string
	Body                map[string]any
}

// fakeAPI answers each "METHOD /path" with a canned body (a missing route answers 404) and records
// every call.
type fakeAPI struct {
	mu     sync.Mutex
	calls  []call
	routes map[string]string
	status map[string]int
	seq    map[string][]int // statuses answered in turn before status applies
}

func newFakeAPI(t *testing.T, routes map[string]string) (*fakeAPI, *sdk.Client) {
	t.Helper()
	f := &fakeAPI{routes: routes, status: map[string]int{}, seq: map[string][]int{}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if b, _ := io.ReadAll(r.Body); len(b) > 0 {
			_ = json.Unmarshal(b, &body)
		}
		key := r.Method + " " + r.URL.Path
		f.mu.Lock()
		f.calls = append(f.calls, call{Method: r.Method, Path: r.URL.Path, Query: r.URL.RawQuery, Body: body})
		out, ok := f.routes[key]
		code := f.status[key]
		if q := f.seq[key]; len(q) > 0 {
			code, f.seq[key] = q[0], q[1:]
		}
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch {
		case !ok:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"success":false,"msg":"not found"}`))
		case code != 0:
			w.WriteHeader(code)
			_, _ = w.Write([]byte(out))
		default:
			_, _ = w.Write([]byte(out))
		}
	}))
	t.Cleanup(srv.Close)
	c := sdk.NewClient(srv.URL)
	c.SetAccessToken("test-token", "", 3600, "write")
	return f, c
}

func (f *fakeAPI) find(method, path string) (call, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.calls {
		if c.Method == method && c.Path == path {
			return c, true
		}
	}
	return call{}, false
}

func noErrors(t *testing.T, what string, diags diag.Diagnostics) {
	t.Helper()
	if diags.HasError() {
		t.Fatalf("%s: %v", what, diags)
	}
}

// Fix 14: a key pair looked up by id is read by that id (the read used the empty d.Id()).
func TestKeyPairDataSource_ByID(t *testing.T) {
	_, c := newFakeAPI(t, map[string]string{
		"GET /api/key-pairs/73": `{"keyPair":{"id":73,"name":"TEST","publicKey":"ssh-ed25519 AAAA test"}}`,
	})
	r := dataSourceKeyPair()
	d := schema.TestResourceDataRaw(t, r.Schema, map[string]any{"id": 73})
	noErrors(t, "read", r.ReadContext(t.Context(), d, c))
	if d.Id() != "73" || d.Get("name") != "TEST" {
		t.Errorf("got id %q name %q, want 73 TEST", d.Id(), d.Get("name"))
	}
}

// Fix 15: the list shows domains shared from the master tenant, whose own read answers 404
// (CLI, 2026-11-17: /api/networks/domains/16 is 404, the list has it). Both lookups use the list.
func TestDomainDataSource_SharedDomain(t *testing.T) {
	routes := map[string]string{
		"GET /api/networks/domains": `{"networkDomains":[{"id":16,"name":"example.com","visibility":"private","active":true},` +
			`{"id":1,"name":"localdomain","visibility":"public","active":true}],"meta":{"total":2}}`,
	}
	for name, raw := range map[string]map[string]any{"by id": {"id": 16}, "by name": {"name": "Example.com"}} {
		t.Run(name, func(t *testing.T) {
			_, c := newFakeAPI(t, routes)
			r := dataSourceDomain()
			d := schema.TestResourceDataRaw(t, r.Schema, raw)
			noErrors(t, "read", r.ReadContext(t.Context(), d, c))
			if d.Id() != "16" || d.Get("name") != "example.com" || d.Get("visibility") != "private" {
				t.Errorf("got id %q name %q visibility %q", d.Id(), d.Get("name"), d.Get("visibility"))
			}
		})
	}
	_, c := newFakeAPI(t, routes)
	r := dataSourceDomain()
	d := schema.TestResourceDataRaw(t, r.Schema, map[string]any{"id": 99})
	if diags := r.ReadContext(t.Context(), d, c); !diags.HasError() || !strings.Contains(diags[0].Summary, "99") {
		t.Errorf("an id in neither the read nor the list: %v, want an error naming 99", diags)
	}
}

// Fix 19.5: a user is made in the login's own tenant, which "Users: Full" allows: no accountId is
// sent (the old default, accountId=1, needed "admin-accounts (full)"). tenant_id may only repeat the
// login's tenant (owner, 2026-11-17).
func TestUserCreate_OwnTenant(t *testing.T) {
	user := `{"user":{"id":9,"username":"tf-acc-u","email":"u@example.com","accountId":9001,"account":{"id":9001},"roles":[{"id":12}]}}`
	routes := map[string]string{
		"GET /api/whoami":  `{"user":{"id":5,"account":{"id":9001,"name":"Acme"}}}`,
		"POST /api/users":  `{"success":true,` + user[1:],
		"GET /api/users/9": user,
	}
	base := map[string]any{"username": "tf-acc-u", "email": "u@example.com", "password": "Xx123456789!", "role_ids": []any{12}}
	with := func(k string, v any) map[string]any {
		m := map[string]any{k: v}
		for k, v := range base {
			m[k] = v
		}
		return m
	}
	r := resourceUser()
	for name, cfg := range map[string]map[string]any{"left out": base, "the login's own": with("tenant_id", 9001)} {
		f, c := newFakeAPI(t, routes)
		d := schema.TestResourceDataRaw(t, r.Schema, cfg)
		noErrors(t, name, r.CreateContext(t.Context(), d, c))
		if post, _ := f.find("POST", "/api/users"); post.Query != "" {
			t.Errorf("%s: query %q, want none (no accountId)", name, post.Query)
		}
		if d.Get("tenant_id") != 9001 {
			t.Errorf("%s: tenant_id = %v, want the login's tenant 9001", name, d.Get("tenant_id"))
		}
	}

	f, c := newFakeAPI(t, routes)
	d := schema.TestResourceDataRaw(t, r.Schema, with("tenant_id", 1))
	diags := r.CreateContext(t.Context(), d, c)
	if !diags.HasError() || !strings.Contains(diags[0].Summary, "isn't the tenant you are logged in to (9001)") {
		t.Errorf("another tenant: %v, want it refused", diags)
	}
	if _, posted := f.find("POST", "/api/users"); posted {
		t.Error("another tenant: the user was sent anyway")
	}
}

// Fix 19.1: a checkbox's default is sent from default_checked, or from default_value "true"/"false"
// (as the console shows it; sending only default_checked dropped "true": live QA 2026-11-17).
// Fix 19.2: MTN's delete of a form with field groups fails on a database constraint; the groups are
// then removed and the delete tried again. A login that may not change forms (403, live) is told what
// to do.
func TestForm_CheckboxAndFieldGroups(t *testing.T) {
	form := `{"optionTypeForm":{"id":5,"name":"f","code":"f","options":[],"fieldGroups":[]}}`
	routes := map[string]string{
		"POST /api/library/option-type-forms":     `{"success":true,` + form[1:],
		"GET /api/library/option-type-forms/5":    form,
		"PUT /api/library/option-type-forms/5":    `{"success":true,` + form[1:],
		"DELETE /api/library/option-type-forms/5": `{"success":true,"msg":"Error on delete form: could not execute batch; SQL [delete from option_type_field_group where id=?]"}`,
		"GET /api/catalog-item-types":             `{"catalogItemTypes":[],"meta":{"total":0}}`,
	}
	r := resourceForm()
	box := map[string]any{"name": "backup", "code": "backup", "field_name": "backup", "field_label": "Backup", "type": "checkbox", "default_checked": true}
	byValue := map[string]any{"name": "b2", "code": "b2", "field_name": "b2", "field_label": "B2", "type": "checkbox", "default_value": "true"}
	f, c := newFakeAPI(t, routes)
	d := schema.TestResourceDataRaw(t, r.Schema, map[string]any{"name": "f", "code": "f", "option_type": []any{box, byValue}})
	noErrors(t, "create", r.CreateContext(t.Context(), d, c))
	post, _ := f.find("POST", "/api/library/option-type-forms")
	opts, _ := post.Body["optionTypeForm"].(map[string]any)["options"].([]any)
	if len(opts) != 2 || opts[0].(map[string]any)["defaultValue"] != true || opts[1].(map[string]any)["defaultValue"] != true {
		t.Errorf("checkboxes sent as %v, want defaultValue true for both", opts)
	}

	grouped := map[string]any{"name": "f", "code": "f", "field_group": []any{map[string]any{"name": "g", "option_type": []any{box}}}}
	// The delete fails on the field groups, they are removed, and the delete is tried again.
	f, c = newFakeAPI(t, routes)
	f.seq["DELETE /api/library/option-type-forms/5"] = []int{http.StatusBadRequest}
	d = schema.TestResourceDataRaw(t, r.Schema, grouped)
	d.SetId("5")
	noErrors(t, "delete", r.DeleteContext(t.Context(), d, c))
	put, ok := f.find("PUT", "/api/library/option-type-forms/5")
	if !ok {
		t.Fatal("delete didn't remove the field groups after the constraint error")
	}
	if g, _ := put.Body["optionTypeForm"].(map[string]any)["fieldGroups"].([]any); g == nil || len(g) != 0 {
		t.Errorf("field groups sent as %v, want []", put.Body)
	}
	if d.Id() != "" {
		t.Error("delete: still in state")
	}

	// The login may not change forms: the error says what to do.
	f, c = newFakeAPI(t, routes)
	f.status["DELETE /api/library/option-type-forms/5"] = http.StatusBadRequest
	f.status["PUT /api/library/option-type-forms/5"] = http.StatusForbidden
	d = schema.TestResourceDataRaw(t, r.Schema, grouped)
	d.SetId("5")
	diags := r.DeleteContext(t.Context(), d, c)
	if !diags.HasError() || !strings.Contains(diags[0].Summary, "may create forms but not change them") ||
		!strings.Contains(diags[0].Summary, "Remove the field groups in the console") {
		t.Errorf("forbidden clear: %v", diags)
	}

	// A form without field groups is deleted with one call.
	f, c = newFakeAPI(t, routes)
	d = schema.TestResourceDataRaw(t, r.Schema, map[string]any{"name": "f", "code": "f"})
	d.SetId("5")
	noErrors(t, "plain delete", r.DeleteContext(t.Context(), d, c))
	if _, put := f.find("PUT", "/api/library/option-type-forms/5"); put {
		t.Error("plain delete: sent a PUT")
	}
}

// Fix 18.1: the nested workflow is picked from the workflows MTN offers for nesting, by name or by
// id, and both are sent as the console sends them.
func TestNestedWorkflowTask_PicksWorkflow(t *testing.T) {
	task := `{"task":{"id":3,"name":"n","taskType":{"code":"nestedWorkflow"},"taskOptions":{"operationalWorkflowId":"61","operationalWorkflowName":"Create Linux User Git"}}}`
	routes := map[string]string{
		"GET /api/options/operationWorkflows": `{"success":true,"data":[{"name":"Create Linux User Git","value":61},{"name":"Install LetsEncrypt SSL Git","value":143}]}`,
		"POST /api/tasks":                     `{"success":true,` + task[1:],
		"GET /api/tasks/3":                    task,
	}
	for name, raw := range map[string]map[string]any{
		"by name": {"name": "n", "operational_workflow_name": "create linux user git"},
		"by id":   {"name": "n", "operational_workflow_id": 61},
	} {
		t.Run(name, func(t *testing.T) {
			f, c := newFakeAPI(t, routes)
			r := resourceNestedWorkflowTask()
			d := schema.TestResourceDataRaw(t, r.Schema, raw)
			noErrors(t, "create", r.CreateContext(t.Context(), d, c))
			post, _ := f.find("POST", "/api/tasks")
			opts := post.Body["task"].(map[string]any)["taskOptions"].(map[string]any)
			if opts["operationalWorkflowId"] != "61" || opts["operationalWorkflowName"] != "Create Linux User Git" {
				t.Errorf("taskOptions = %v, want id \"61\" and the workflow's name", opts)
			}
		})
	}
	_, c := newFakeAPI(t, routes)
	r := resourceNestedWorkflowTask()
	d := schema.TestResourceDataRaw(t, r.Schema, map[string]any{"name": "n", "operational_workflow_id": 177})
	diags := r.CreateContext(t.Context(), d, c)
	if !diags.HasError() || !strings.Contains(diags[0].Summary, "Available: Create Linux User Git, Install LetsEncrypt SSL Git") {
		t.Errorf("a workflow MTN doesn't offer: %v, want the ones it does", diags)
	}
}

// Fix 17 and 19 (minor): checked at plan.
func TestPlanChecks_CronRowsAndDates(t *testing.T) {
	cases := []struct {
		name string
		r    *schema.Resource
		raw  map[string]any
		bad  bool
	}{
		{"cron in words", resourceExecuteSchedule(), map[string]any{"name": "s", "time_zone": "UTC", "schedule": "every day"}, true},
		{"cron", resourceExecuteSchedule(), map[string]any{"name": "s", "time_zone": "UTC", "schedule": "0 2 * * *"}, false},
		{"five words", resourceExecuteSchedule(), map[string]any{"name": "s", "time_zone": "UTC", "schedule": "every day at two am"}, true},
		{"cron names", resourceExecuteSchedule(), map[string]any{"name": "s", "time_zone": "UTC", "schedule": "0 9 ? JAN-JUN mon-fri"}, false},
		{"cron L and #", resourceExecuteSchedule(), map[string]any{"name": "s", "time_zone": "UTC", "schedule": "0 0 L * 5#2"}, false},
		{"job date in words", resourceWorkflowJob(), map[string]any{"name": "j", "workflow_id": 1, "context_type": "appliance", "schedule_mode": "date_and_time", "scheduled_date_and_time": "tomorrow"}, true},
		{"job date", resourceWorkflowJob(), map[string]any{"name": "j", "workflow_id": 1, "context_type": "appliance", "schedule_mode": "date_and_time", "scheduled_date_and_time": "2026-12-01T09:00:00Z"}, false},
		{"zero text rows", resourceForm(), map[string]any{"name": "f", "code": "f",
			"option_type": []any{map[string]any{"name": "n", "type": "textarea", "text_rows": 0}}}, true},
	}
	for _, tc := range cases {
		diags := tc.r.Validate(terraform.NewResourceConfigRaw(tc.raw))
		if diags.HasError() != tc.bad {
			t.Errorf("%s: errors %v, want an error: %v", tc.name, diags, tc.bad)
		}
	}
}

// Fix 18 (minor): a failed update keeps the state it started from, not the planned values.
func TestFailedUpdateKeepsState(t *testing.T) {
	f, c := newFakeAPI(t, map[string]string{"PUT /api/execute-schedules/5": `{"success":false,"msg":"Access Restricted"}`})
	f.status["PUT /api/execute-schedules/5"] = http.StatusForbidden
	p := &schema.Provider{ResourcesMap: map[string]*schema.Resource{"mtncloud_execute_schedule": resourceExecuteSchedule()}}
	withKeptStateOnFailedUpdate(p)
	r := p.ResourcesMap["mtncloud_execute_schedule"]
	d := schema.TestResourceDataRaw(t, r.Schema, map[string]any{"name": "new-name", "time_zone": "UTC", "schedule": "0 3 * * *"})
	d.SetId("5")
	if diags := r.UpdateContext(t.Context(), d, c); !diags.HasError() {
		t.Fatal("update: want the 403 back")
	}
	if got := d.State().Attributes["name"]; got == "new-name" {
		t.Errorf("state name = %q after a failed update, want the planned value left out", got)
	}
}

// A public key read with file() ends in a newline that MTN Cloud doesn't store: no diff, so the
// plan-time "can't change" check doesn't block the next plan or the destroy (live QA, 2026-11-17).
func TestSameKey(t *testing.T) {
	k := "ssh-ed25519 AAAAC3Nza tf-acc"
	for b, want := range map[string]bool{k + "\n": true, k + "\r\n": true, "ssh-ed25519  AAAAC3Nza tf-acc": true, "ssh-ed25519 AAAAC3Nzb tf-acc": false} {
		if sameKey(k, b) != want {
			t.Errorf("sameKey(%q, %q) = %v, want %v", k, b, !want, want)
		}
	}
	if !resourceKeyPair().Schema["public_key"].DiffSuppressFunc("public_key", k, k+"\n", nil) {
		t.Error("public_key: a trailing newline shows as a change")
	}
	// The whole diff, the plan-time "can't change" check included: no error and no change.
	r := resourceKeyPair()
	state := &terraform.InstanceState{ID: "5", Attributes: map[string]string{"id": "5", "name": "kp", "public_key": k}}
	for cfg, wantErr := range map[string]bool{k + "\n": false, "ssh-ed25519 AAAAC3Nzb tf-acc\n": true} {
		diff, err := r.SimpleDiff(t.Context(), state, terraform.NewResourceConfigRaw(map[string]any{"name": "kp", "public_key": cfg}), nil)
		if (err != nil) != wantErr {
			t.Errorf("public_key %q: error %v, want error %v", cfg, err, wantErr)
		}
		if !wantErr && diff != nil && len(diff.Attributes) > 0 {
			t.Errorf("public_key %q: diff %v, want none", cfg, diff.Attributes)
		}
	}
}
