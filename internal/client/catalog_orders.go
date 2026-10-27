package client

import (
	"context"
	"net/http"
	"strings"
)

// OrderedItem is one item of a catalog order, as the inventory (/api/catalog/items) shows it. A
// workflow item (a project, MOS, ...) runs an execution; it is null until the workflow starts.
type OrderedItem struct {
	ID            int64
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
	it := &OrderedItem{ID: w.ID, TypeName: w.Type.Name, Status: w.Status, StatusMessage: deref(w.StatusMessage)}
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
