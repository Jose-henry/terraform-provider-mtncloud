package client

import (
	"context"
	"net/http"
	"net/url"
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
	InstanceIDs []int64
}

// ListApps returns every app the caller can see, with its instances.
func (c *Client) ListApps(ctx context.Context) ([]App, error) {
	type wireApp struct {
		ID        int64  `json:"id"`
		Name      string `json:"name"`
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
		out[i] = App{ID: a.ID, Name: a.Name}
		for _, in := range a.Instances {
			out[i].InstanceIDs = append(out[i].InstanceIDs, in.ID)
		}
	}
	return out, nil
}

// DeleteApp deletes an app with its instances, their disks and floating IPs. One already gone
// counts as deleted.
func (c *Client) DeleteApp(ctx context.Context, id int64) error {
	q := url.Values{"removeInstances": {"on"}, "releaseFloatingIps": {"on"}}
	return ignoreNotFound(c.do(ctx, http.MethodDelete, "/api/apps/"+itoa(id), q, nil, nil))
}
