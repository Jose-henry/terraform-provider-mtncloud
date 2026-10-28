package client

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// FormField is one input of a catalog item's order form.
type FormField struct {
	ID       int64  // the option type ID: select values come from /api/options/list?optionTypeId=<ID>
	Name     string // the field name the workflow reads, for example "mysqlVersion"
	Type     string // widget: text, select, checkbox, number, password, diskManager, ...
	LayoutID int64  // the layout the form's plan list is built for, when the field names one
}

// CatalogForm is a catalog item's order form, read live so no option type ID is ever hard-coded.
type CatalogForm struct {
	ItemID  int64
	Name    string
	Context string // "appliance" or "instance"
	Fields  map[string]FormField
}

// LayoutID is the layout the form's plans are listed for: the first field that names one.
func (f *CatalogForm) LayoutID(fields ...string) int64 {
	for _, n := range fields {
		if x, ok := f.Fields[n]; ok && x.LayoutID != 0 {
			return x.LayoutID
		}
	}
	return 0
}

// CatalogForm reads the order form of the catalog item with this name (cached per provider).
func (c *Client) CatalogForm(ctx context.Context, itemName string) (*CatalogForm, error) {
	return cached(ctx, &c.cache, "catalog-form:"+itemName, func(ctx context.Context) (*CatalogForm, error) {
		items, err := c.CatalogItems(ctx)
		if err != nil {
			return nil, err
		}
		var id int64
		for _, it := range items {
			if it.Name == itemName {
				id = it.ID
			}
		}
		if id == 0 {
			return nil, &NoMatchError{msg: fmt.Sprintf("no catalog item named %q in your catalog", itemName)}
		}
		var resp struct {
			Item struct {
				Name        string          `json:"name"`
				Context     string          `json:"context"`
				Form        json.RawMessage `json:"form"`
				OptionTypes json.RawMessage `json:"optionTypes"`
			} `json:"catalogItemType"`
		}
		if err := c.get(ctx, "/api/catalog/types/"+itoa(id), nil, &resp); err != nil {
			return nil, err
		}
		form := &CatalogForm{ItemID: id, Name: resp.Item.Name, Context: resp.Item.Context, Fields: map[string]FormField{}}
		for _, raw := range []json.RawMessage{resp.Item.Form, resp.Item.OptionTypes} {
			var v any
			if len(raw) > 0 && json.Unmarshal(raw, &v) == nil {
				collectFields(v, form.Fields)
			}
		}
		return form, nil
	})
}

// collectFields walks a form (options, fieldGroups[].options) and keeps every input by field name.
func collectFields(v any, out map[string]FormField) {
	switch x := v.(type) {
	case map[string]any:
		if name, ok := x["fieldName"].(string); ok && name != "" {
			f := FormField{Name: name}
			f.ID, _ = asInt64(x["id"])
			f.Type, _ = x["type"].(string)
			if cfg, ok := x["config"].(map[string]any); ok {
				f.LayoutID, _ = asInt64(cfg["layoutId"])
			}
			out[name] = f
		}
		for _, e := range x {
			collectFields(e, out)
		}
	case []any:
		for _, e := range x {
			collectFields(e, out)
		}
	}
}

func asInt64(v any) (int64, bool) {
	switch n := v.(type) {
	case float64:
		return int64(n), true
	case string:
		i, err := strconv.ParseInt(n, 10, 64)
		return i, err == nil
	}
	return 0, false
}

// FormOptions lists the choices of a select field (name shown, value sent).
func (c *Client) FormOptions(ctx context.Context, optionTypeID int64) ([]NamedValue, error) {
	q := url.Values{"optionTypeId": {itoa(optionTypeID)}}
	return cached(ctx, &c.cache, "form-options?"+q.Encode(), func(ctx context.Context) ([]NamedValue, error) {
		var resp struct {
			Data []struct {
				Name  string `json:"name"`
				Value any    `json:"value"`
			} `json:"data"`
		}
		if err := c.get(ctx, "/api/options/list", q, &resp); err != nil {
			return nil, err
		}
		out := make([]NamedValue, 0, len(resp.Data))
		for _, d := range resp.Data {
			out = append(out, NamedValue{Name: d.Name, Value: strings.TrimSpace(fmt.Sprint(d.Value))})
		}
		return out, nil
	})
}

// FormChoice finds a select field's choice by its value or its shown name (case ignored). The
// error lists what the field offers.
func (c *Client) FormChoice(ctx context.Context, f FormField, what, want string) (NamedValue, error) {
	opts, err := c.FormOptions(ctx, f.ID)
	if err != nil {
		return NamedValue{}, err
	}
	for _, o := range opts {
		// Some lists quote their values ('Replace'): match without the quotes too.
		if o.Value == want || strings.Trim(o.Value, "'") == want {
			return o, nil
		}
	}
	for _, o := range opts {
		if strings.EqualFold(o.Name, want) || strings.EqualFold(strings.Trim(o.Value, "'"), want) {
			return o, nil
		}
	}
	names := make([]string, 0, len(opts))
	for _, o := range opts {
		names = append(names, strings.Trim(o.Value, "'"))
	}
	return NamedValue{}, &NoMatchError{msg: fmt.Sprintf("no %s %q: choose one of %s", what, want, strings.Join(names, ", "))}
}

// OrderForInstance orders a catalog item that runs against an existing instance (context
// "instance"), with its form values in config.
func (c *Client) OrderForInstance(ctx context.Context, typeName string, instanceID int64, config map[string]any) (*OrderedItem, error) {
	body := map[string]any{"order": map[string]any{"items": []any{map[string]any{
		"type":    map[string]any{"name": typeName},
		"config":  config,
		"context": "instance",
		"targets": []any{map[string]any{"id": itoa(instanceID)}},
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
