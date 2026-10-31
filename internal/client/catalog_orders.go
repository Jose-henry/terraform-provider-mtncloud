package client

import (
	"context"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

// OrderedItem is one item of a catalog order, as the inventory (/api/catalog/items) shows it. A
// workflow item (a project, MOS, ...) runs an execution; it is null until the workflow starts.
type OrderedItem struct {
	ID            int64
	TypeID        int64  // the catalog item's ID, which a rename keeps
	TypeName      string // the catalog item type, for example "Create Project"
	Status        string // the item's own status, for example "ORDERED"; it doesn't follow the execution
	StatusMessage string
	Execution     *Execution
}

// Execution is a workflow item's run: "success" or "error" when it has finished.
type Execution struct {
	ID     int64
	Status string
}

type wireOrderedItem struct {
	ID   int64 `json:"id"`
	Type struct {
		ID   int64  `json:"id"`
		Name string `json:"name"`
	} `json:"type"`
	Status        string  `json:"status"`
	StatusMessage *string `json:"statusMessage"`
	Execution     *struct {
		ID     int64  `json:"id"`
		Status string `json:"status"`
	} `json:"execution"`
}

func (w wireOrderedItem) item() *OrderedItem {
	it := &OrderedItem{ID: w.ID, TypeID: w.Type.ID, TypeName: w.Type.Name, Status: w.Status, StatusMessage: deref(w.StatusMessage)}
	if w.Execution != nil {
		it.Execution = &Execution{ID: w.Execution.ID, Status: w.Execution.Status}
	}
	return it
}

// OrderWorkflow orders one workflow catalog item that runs on the appliance (no target instance),
// with its form values in config. It returns the ordered item. The order is placed at once: there
// is no cart, so a retry after an unclear failure must look for the item before ordering again.
func (c *Client) OrderWorkflow(ctx context.Context, typeName string, config map[string]any) (*OrderedItem, error) {
	body := map[string]any{"order": map[string]any{"items": []any{map[string]any{
		"type":    map[string]any{"name": typeName},
		"config":  config,
		"context": "appliance",
	}}}}
	var resp struct {
		Order struct {
			Items []wireOrderedItem `json:"items"`
		} `json:"order"`
	}
	if err := c.do(ctx, http.MethodPost, "/api/catalog/orders", nil, body, &resp); err != nil {
		return nil, err
	}
	if len(resp.Order.Items) != 1 {
		return nil, &APIError{StatusCode: http.StatusOK, Method: http.MethodPost, Path: "/api/catalog/orders",
			Message: "the order response has no item. Check Catalog > Inventory in the console before ordering again"}
	}
	return resp.Order.Items[0].item(), nil
}

// GetOrderedItem reads one inventory item.
func (c *Client) GetOrderedItem(ctx context.Context, id int64) (*OrderedItem, error) {
	var resp struct {
		Item wireOrderedItem `json:"item"`
	}
	if err := c.get(ctx, "/api/catalog/items/"+itoa(id), nil, &resp); err != nil {
		return nil, err
	}
	return resp.Item.item(), nil
}

// ExecutionDone reports whether a workflow execution has finished, and whether it succeeded.
func ExecutionDone(e *Execution) (done, ok bool) {
	if e == nil {
		return false, false
	}
	switch strings.ToLower(e.Status) {
	case "success", "complete", "completed":
		return true, true
	case "error", "failed", "failure", "cancelled", "canceled": //nolint:misspell // both spellings are API values
		return true, false
	}
	return false, false
}

// WaitForExecution polls a workflow item until its execution finishes, success or failure. It never
// waits for what the workflow made to sync into MTN Cloud (D-020). A 404 straight after the order is
// read as not there yet.
func (c *Client) WaitForExecution(ctx context.Context, id int64) (*OrderedItem, error) {
	return poll(ctx, c, func(ctx context.Context) (*OrderedItem, error) { return c.GetOrderedItem(ctx, id) },
		func(it *OrderedItem, err error) (bool, error) {
			switch {
			case IsNotFound(err):
				return false, nil
			case err != nil:
				return false, err
			}
			done, _ := ExecutionDone(it.Execution)
			return done || strings.EqualFold(it.Status, "failed"), nil
		})
}

// DeleteOrderedItem deletes an order's record from the inventory (DELETE /api/catalog/items/{id}).
// A workflow order isn't linked to what its workflow made, so this removes only the record: delete
// the instances or apps first. One already gone counts as deleted.
func (c *Client) DeleteOrderedItem(ctx context.Context, id int64) error {
	return ignoreNotFound(c.do(ctx, http.MethodDelete, "/api/catalog/items/"+itoa(id), nil, nil, nil))
}

// App is an app with the instances it holds.
type App struct {
	ID          int64
	Name        string
	Status      string // "pending" while it provisions, then "running", "warning", "failed"...
	InstanceIDs []int64
}

// ListApps returns every app the caller can see, with its instances.
func (c *Client) ListApps(ctx context.Context) ([]App, error) {
	type wireApp struct {
		ID        int64  `json:"id"`
		Name      string `json:"name"`
		Status    string `json:"status"`
		Instances []struct {
			ID int64 `json:"id"`
		} `json:"instances"`
	}
	apps, err := listAll[wireApp](ctx, c, "/api/apps", "apps", nil)
	if err != nil {
		return nil, err
	}
	out := make([]App, len(apps))
	for i, a := range apps {
		out[i] = App{ID: a.ID, Name: a.Name, Status: a.Status}
		for _, in := range a.Instances {
			out[i].InstanceIDs = append(out[i].InstanceIDs, in.ID)
		}
	}
	return out, nil
}

// AppsNamed is every app with this name (case ignored): names aren't unique across projects.
func AppsNamed(apps []App, name string) []App {
	var out []App
	for _, a := range apps {
		if strings.EqualFold(a.Name, name) {
			out = append(out, a)
		}
	}
	return out
}

// AppRunning reports whether a settled app came up: for a new app, any other status (failed,
// error, warning, canceled...) is a failure.
func AppRunning(a *App) bool { return strings.EqualFold(a.Status, "running") }

// AppSettled reports whether an app has finished provisioning, successfully or not.
func AppSettled(a *App) bool {
	switch strings.ToLower(a.Status) {
	case "", "pending", "provisioning", "deploying":
		return false
	}
	return true
}

// WaitForApp waits for the app an order's workflow started, named after the order, to finish
// provisioning. The workflow ends as soon as it asks for the app (managed services, QA
// 2026-10-31), and its VMs are named from MTN's template, not after the order. An app in before
// (one with the name that was there before the order) is never taken; of new ones, the newest.
func (c *Client) WaitForApp(ctx context.Context, name string, before map[int64]bool) (*App, error) {
	return poll(ctx, c, func(ctx context.Context) (*App, error) {
		apps, err := c.ListApps(ctx)
		if err != nil {
			return nil, err
		}
		var got *App
		for _, a := range AppsNamed(apps, name) {
			if !before[a.ID] && (got == nil || a.ID > got.ID) {
				got = &a
			}
		}
		return got, nil
	}, func(a *App, err error) (bool, error) {
		if err != nil {
			return false, err
		}
		return a != nil && AppSettled(a), nil
	})
}

// DeleteApp deletes an app with its instances, their disks and floating IPs. One already gone
// counts as deleted.
func (c *Client) DeleteApp(ctx context.Context, id int64) error {
	q := url.Values{"removeInstances": {"on"}, "releaseFloatingIps": {"on"}}
	return ignoreNotFound(c.do(ctx, http.MethodDelete, "/api/apps/"+itoa(id), q, nil, nil))
}

// RemoveAppInstance takes an instance out of an app and leaves it running
// (POST /api/apps/{id}/remove-instance, body {"instanceId": N}; CLI `apps remove-instance -d`,
// 2026-10-31). An app or instance already gone counts as done.
func (c *Client) RemoveAppInstance(ctx context.Context, appID, instanceID int64) error {
	body := map[string]any{"instanceId": instanceID}
	return ignoreNotFound(c.do(ctx, http.MethodPost, "/api/apps/"+itoa(appID)+"/remove-instance", nil, body, nil))
}

// ExecutionFailure is what a failed workflow run says about why: the last lines of its failed
// task's output, for example "Error: kafkaVersion must be 4.3.1 or 4.2.1 (got: 'null')." (QA,
// 2026-10-31). The order's own secret values are masked first, and a line that mentions a secret
// is left out. Empty when there is nothing to show or the run can't be read.
func (c *Client) ExecutionFailure(ctx context.Context, executionID int64, secrets []string) string {
	var resp struct {
		Execution struct {
			Process struct {
				Events []struct {
					Status string `json:"status"`
					Output string `json:"output"`
					Error  string `json:"error"`
				} `json:"events"`
			} `json:"process"`
		} `json:"jobExecution"`
	}
	if executionID == 0 || c.get(ctx, "/api/job-executions/"+itoa(executionID), nil, &resp) != nil {
		return ""
	}
	for _, e := range resp.Execution.Process.Events {
		if !strings.EqualFold(e.Status, "failed") && !strings.EqualFold(e.Status, "error") {
			continue
		}
		text := e.Error + "\n" + e.Output
		for _, v := range secrets {
			if v != "" {
				text = strings.ReplaceAll(text, v, "***")
			}
		}
		// A task set can fail with no output of its own: its failed task has the reason.
		if out := lastLines(text, 3); out != "" {
			return out
		}
	}
	return ""
}

// ExecutionOutput is everything a workflow execution's tasks printed, in order.
func (c *Client) ExecutionOutput(ctx context.Context, executionID int64) (string, error) {
	var resp struct {
		Execution struct {
			Process struct {
				Events []struct {
					Output string `json:"output"`
				} `json:"events"`
			} `json:"process"`
		} `json:"jobExecution"`
	}
	if err := c.get(ctx, "/api/job-executions/"+itoa(executionID), nil, &resp); err != nil {
		return "", err
	}
	var b strings.Builder
	for _, e := range resp.Execution.Process.Events {
		b.WriteString(e.Output)
		b.WriteString("\n")
	}
	return b.String(), nil
}

var (
	reportedID   = regexp.MustCompile(`/provisioning/instances/(\d+)`)
	reportedName = regexp.MustCompile(`(?m)^\s*Instance name\s*:\s*(\S+)\s*$`)
)

// WorkflowInstance is the VM a workflow that names it itself reports in its output: its ID, from
// the console link it prints last, and its name (MTN's SOPHOS script, `Sophos/sophosheat.sh`:
// " Instance name : <name>", and the instance's console link "<url>/provisioning/instances/<id>"). Zero and ""
// when it printed neither.
func WorkflowInstance(output string) (id int64, name string) {
	if m := reportedID.FindAllStringSubmatch(output, -1); len(m) > 0 {
		id, _ = strconv.ParseInt(m[len(m)-1][1], 10, 64)
	}
	if m := reportedName.FindAllStringSubmatch(output, -1); len(m) > 0 {
		name = m[len(m)-1][1]
	}
	return id, name
}

// secretWord marks an output line that may carry a secret: a word for one, a masked value, or a
// password flag ("mysql -pS3cret").
var secretWord = regexp.MustCompile(`(?i)pass|pwd|secret|token|key|credential|auth|private|\*\*\*|\s-p\S`)

// lastLines is the last n non-empty lines of s that don't mention a secret, joined, at most 500
// characters.
func lastLines(s string, n int) string {
	var keep []string
	lines := strings.Split(s, "\n")
	for i := len(lines) - 1; i >= 0 && len(keep) < n; i-- {
		if l := strings.TrimSpace(lines[i]); l != "" && !secretWord.MatchString(l) {
			keep = append([]string{l}, keep...)
		}
	}
	out := strings.Join(keep, " ")
	if len(out) > 500 {
		out = "..." + out[len(out)-497:]
	}
	return out
}
