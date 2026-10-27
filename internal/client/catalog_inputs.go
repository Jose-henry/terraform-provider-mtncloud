package client

import (
	"cmp"
	"context"
	"fmt"
	"slices"
)

// CatalogInput is one input of a catalog item's order form. Shapes from a customer's read of
// GET /api/catalog/types/{id} (2026-10-27): inputs sit in "optionTypes" (formType "optionTypes")
// or in "form": its "options" and each "fieldGroups[].options" (formType "form").
type CatalogInput struct {
	Name         string // fieldName: the key to give it when ordering
	Label        string
	Type         string // text, number, select, checkbox, group, cloud...
	Required     bool
	Default      string
	Help         string
	DisplayOrder int64
}

type wireInput struct {
	FieldName    string `json:"fieldName"`
	FieldLabel   string `json:"fieldLabel"`
	Type         string `json:"type"`
	Required     bool   `json:"required"`
	DefaultValue any    `json:"defaultValue"`
	HelpBlock    string `json:"helpBlock"`
	DisplayOrder int64  `json:"displayOrder"`
}

// CatalogItemInputs reads the inputs of one catalog item, in the order the console shows them.
func (c *Client) CatalogItemInputs(ctx context.Context, id int64) ([]CatalogInput, error) {
	var resp struct {
		Item struct {
			OptionTypes []wireInput `json:"optionTypes"`
			Form        *struct {
				Options     []wireInput `json:"options"`
				FieldGroups []struct {
					Options []wireInput `json:"options"`
				} `json:"fieldGroups"`
			} `json:"form"`
		} `json:"catalogItemType"`
	}
	if err := c.get(ctx, "/api/catalog/types/"+itoa(id), nil, &resp); err != nil {
		return nil, err
	}
	all := resp.Item.OptionTypes
	if f := resp.Item.Form; f != nil {
		all = append(all, f.Options...)
		for _, g := range f.FieldGroups {
			all = append(all, g.Options...)
		}
	}
	out := make([]CatalogInput, 0, len(all))
	for _, w := range all {
		if w.FieldName == "" {
			continue
		}
		def := ""
		if w.DefaultValue != nil {
			def = fmt.Sprint(w.DefaultValue)
		}
		out = append(out, CatalogInput{
			Name: w.FieldName, Label: w.FieldLabel, Type: w.Type, Required: w.Required,
			Default: def, Help: w.HelpBlock, DisplayOrder: w.DisplayOrder,
		})
	}
	slices.SortStableFunc(out, func(a, b CatalogInput) int {
		return cmp.Or(cmp.Compare(a.DisplayOrder, b.DisplayOrder), cmp.Compare(a.Name, b.Name))
	})
	return out, nil
}
