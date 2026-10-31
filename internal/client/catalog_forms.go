package client

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

// FormField is one input of a catalog item's order form, with the rules the console applies.
type FormField struct {
	ID       int64  // the option type ID: select values come from /api/options/list?optionTypeId=<ID>
	Name     string // the field name the workflow reads, for example "mysqlVersion"
	Label    string // what the console shows, for example "VOLUMES"
	Type     string // widget: text, select, checkbox, number, password, diskManager, ...
	LayoutID int64  // the layout the form's plan list is built for, when the field names one
	Required bool
	Hidden   bool // not shown: the console sends Default
	Default  any  // the console's pre-filled value (text, a number, or JSON text for disks), nil for none
	// RequireOn and VisibleOn are the field's conditions ("enableAuth", "db_engine:mysql"): it is
	// required (or shown) only while another field has that value. Empty: always.
	RequireOn, VisibleOn string
	Help                 string // the console's help text under the field
	// Locked: shown but not editable in the console, which sends Default. NoBlank: a value must not
	// be empty. Pattern: the console's check on a typed value (a regular expression, as the form
	// gives it). Min and Max bound a number, when the form sets them. Placeholder is the example the
	// console shows. DependsOn: the field's choices are listed for another field's value.
	Locked, NoBlank      bool
	Pattern, Placeholder string
	Min, Max             *int64
	DependsOn            string
}

// CatalogItemRef names a catalog item by its code, which MTN keeps when it renames an item, and by
// every name it has had (the first is the one shown in messages).
type CatalogItemRef struct {
	Code  string
	Names []string
}

// Name is the item's name for messages.
func (r CatalogItemRef) Name() string {
	if len(r.Names) > 0 {
		return r.Names[0]
	}
	return r.Code
}

// Is reports whether name is one this item has had (case ignored).
func (r CatalogItemRef) Is(name string) bool {
	for _, n := range r.Names {
		if strings.EqualFold(n, name) {
			return true
		}
	}
	return false
}

// CatalogItemType is a catalog item with its code.
type CatalogItemType struct {
	ID   int64
	Code string
	Name string
}

// CatalogItemTypes lists the catalog's items with their codes (/api/catalog-item-types, readable by
// a customer login, 2026-10-30; the customer view /api/catalog/types has no codes). When that list
// is refused, the customer view is used, without codes.
func (c *Client) CatalogItemTypes(ctx context.Context) ([]CatalogItemType, error) {
	return cached(ctx, &c.cache, "catalog-item-types", func(ctx context.Context) ([]CatalogItemType, error) {
		type wire struct {
			ID   int64  `json:"id"`
			Code string `json:"code"`
			Name string `json:"name"`
		}
		items, err := listAll[wire](ctx, c, "/api/catalog-item-types", "catalogItemTypes", nil)
		if err == nil {
			out := make([]CatalogItemType, len(items))
			for i, x := range items {
				out[i] = CatalogItemType(x)
			}
			return out, nil
		}
		if !IsNotFound(err) && !IsForbidden(err) {
			return nil, err
		}
		view, err := c.CatalogItems(ctx)
		if err != nil {
			return nil, err
		}
		out := make([]CatalogItemType, len(view))
		for i, x := range view {
			out[i] = CatalogItemType{ID: x.ID, Name: x.Name}
		}
		return out, nil
	})
}

// FindCatalogItem finds an item by its code, then by any name it has had.
func (c *Client) FindCatalogItem(ctx context.Context, ref CatalogItemRef) (*CatalogItemType, error) {
	items, err := c.CatalogItemTypes(ctx)
	if err != nil {
		return nil, err
	}
	if ref.Code != "" {
		for _, it := range items {
			if strings.EqualFold(it.Code, ref.Code) {
				return &it, nil
			}
		}
	}
	for _, it := range items {
		if ref.Is(it.Name) {
			return &it, nil
		}
	}
	// The coded list may leave out an item the customer catalog shows (one shared from the master
	// tenant, for some roles): look there by name before giving up.
	if view, err := c.CatalogItems(ctx); err == nil {
		for _, x := range view {
			if ref.Is(x.Name) {
				return &CatalogItemType{ID: x.ID, Name: x.Name}, nil
			}
		}
	}
	return nil, &NoMatchError{msg: fmt.Sprintf("no catalog item %q (code %s) in your catalog", ref.Name(), ref.Code)}
}

// CatalogForm is a catalog item's order form, read live so no option type ID is ever hard-coded.
type CatalogForm struct {
	ItemID  int64
	Name    string // the item's name now
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

// CatalogForm reads the order form of a catalog item, found by its code (cached per provider).
func (c *Client) CatalogForm(ctx context.Context, ref CatalogItemRef) (*CatalogForm, error) {
	return cached(ctx, &c.cache, "catalog-form:"+ref.Code+":"+ref.Name(), func(ctx context.Context) (*CatalogForm, error) {
		it, err := c.FindCatalogItem(ctx, ref)
		if err != nil {
			return nil, err
		}
		id := it.ID
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
			f.Label, _ = x["fieldLabel"].(string)
			f.Required, _ = x["required"].(bool)
			f.Hidden, _ = x["isHidden"].(bool)
			f.RequireOn, _ = x["requireOnCode"].(string)
			f.VisibleOn, _ = x["visibleOnCode"].(string)
			f.Help, _ = x["helpBlock"].(string)
			f.Locked, _ = x["isLocked"].(bool)
			f.NoBlank, _ = x["noBlank"].(bool)
			f.Pattern, _ = x["verifyPattern"].(string)
			f.Placeholder, _ = x["placeHolder"].(string)
			f.DependsOn, _ = x["dependsOnCode"].(string)
			if n, ok := asInt64(x["minVal"]); ok {
				f.Min = &n
			}
			if n, ok := asInt64(x["maxVal"]); ok {
				f.Max = &n
			}
			f.Default = x["defaultValue"]
			if cfg, ok := x["config"].(map[string]any); ok {
				f.LayoutID, _ = asInt64(cfg["layoutId"])
				if d, ok := cfg["defaultValue"]; ok && d != nil && d != "" {
					f.Default = d // a disk or network manager's default (JSON text)
				}
			}
			if s, ok := f.Default.(string); ok && s == "" {
				f.Default = nil
			}
			if _, seen := out[name]; !seen {
				out[name] = f
			}
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
	return c.FormOptionsIn(ctx, optionTypeID, nil)
}

// FormOptionsIn lists a select field's choices for a form filled with the given values. Lists that
// depend on other fields (the plans on the project) answer empty without them. The console sends
// the form's values with the item's ID (captured 2026-10-28: plans on item 41 need
// catalogItemTypeId, cloud, the group and resourcepool).
func (c *Client) FormOptionsIn(ctx context.Context, optionTypeID int64, form url.Values) ([]NamedValue, error) {
	q := url.Values{"optionTypeId": {itoa(optionTypeID)}}
	for k, v := range form {
		q[k] = v
	}
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
	return c.FormChoiceIn(ctx, f, nil, what, want)
}

// FormChoiceIn is FormChoice for a field whose choices depend on the form's other values. A choice
// matches by value, then by shown name, then by the size code inside a name: "G2S4" picks
// "Small (G2S4)" on a form that names its plans that way (PfSense, 2026-10-30).
func (c *Client) FormChoiceIn(ctx context.Context, f FormField, form url.Values, what, want string) (NamedValue, error) {
	opts, err := c.FormOptionsIn(ctx, f.ID, form)
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
	var inner []NamedValue
	for _, o := range opts {
		if m := parenCode.FindStringSubmatch(o.Name); m != nil && strings.EqualFold(m[1], want) {
			inner = append(inner, o)
		}
	}
	if len(inner) == 1 {
		return inner[0], nil
	}
	names := make([]string, 0, len(opts))
	for _, o := range opts {
		names = append(names, o.Name)
	}
	if len(names) == 0 {
		return NamedValue{}, &NoMatchError{msg: fmt.Sprintf("no %s %q: the form lists no choices for this project", what, want)}
	}
	return NamedValue{}, &NoMatchError{msg: fmt.Sprintf("no %s %q: choose one of %s", what, want, strings.Join(names, ", "))}
}

// parenCode is the code in a choice's name, for example "G2S4" in "Small (G2S4)".
var parenCode = regexp.MustCompile(`\(([^()]+)\)\s*$`)

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
