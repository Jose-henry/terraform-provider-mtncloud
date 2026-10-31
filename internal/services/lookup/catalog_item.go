package lookup

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/datasourcevalidator"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/mtn/terraform-provider-mtncloud/internal/client"
)

func newCatalogItem() datasource.DataSource { return &catalogItemDataSource{} }

// catalogItemDataSource is mtncloud_catalog_item: one service you can order, with the inputs its
// order form asks for.
type catalogItemDataSource struct{ base }

type oneCatalogItemModel struct {
	ID          types.Int64         `tfsdk:"id"`
	Name        types.String        `tfsdk:"name"`
	Description types.String        `tfsdk:"description"`
	Type        types.String        `tfsdk:"type"`
	Featured    types.Bool          `tfsdk:"featured"`
	Inputs      []catalogInputModel `tfsdk:"inputs"`
}

type catalogInputModel struct {
	Name     types.String `tfsdk:"name"`
	Label    types.String `tfsdk:"label"`
	Type     types.String `tfsdk:"type"`
	Required types.Bool   `tfsdk:"required"`
	Default  types.String `tfsdk:"default"`
	Help     types.String `tfsdk:"help"`
	// The form's rules for the input.
	RequiredWhen types.String `tfsdk:"required_when"`
	ShownWhen    types.String `tfsdk:"shown_when"`
	Hidden       types.Bool   `tfsdk:"hidden"`
	Locked       types.Bool   `tfsdk:"locked"`
	NoBlank      types.Bool   `tfsdk:"no_blank"`
	Pattern      types.String `tfsdk:"pattern"`
	Example      types.String `tfsdk:"example"`
	Min          types.Int64  `tfsdk:"min"`
	Max          types.Int64  `tfsdk:"max"`
}

func (d *catalogItemDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_catalog_item"
}

func (d *catalogItemDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Looks up one service you can order from the MTN Cloud catalog, by ID or by the name shown in the catalog, " +
			"with the inputs its order form asks for and the form's rules for each: required (or required only while " +
			"another input has a value), shown only while another has a value, hidden, locked, a pattern, number bounds, " +
			"an example and the help text. To list the catalog, use `mtncloud_catalog_items`; to see what you have " +
			"ordered, `mtncloud_catalog_orders`.",
		Attributes: map[string]schema.Attribute{
			// Optional+Computed: give id or name, and the other is filled in.
			"id":          schema.Int64Attribute{Optional: true, Computed: true, Description: "Catalog item ID. Give this or `name`."},
			"name":        schema.StringAttribute{Optional: true, Computed: true, Description: "Name shown in the catalog, matched exactly, for example `Create Project`. Give this or `id`."},
			"description": schema.StringAttribute{Computed: true, Description: "Description shown in the catalog."},
			"type":        schema.StringAttribute{Computed: true, Description: "`instance`, `blueprint` or `workflow`."},
			"featured":    schema.BoolAttribute{Computed: true, Description: "Whether the catalog features this item."},
			"inputs": schema.ListNestedAttribute{
				Computed:    true,
				Description: "The inputs of the order form, in the order the console shows them. Empty when the item asks for none.",
				NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
					"name":     schema.StringAttribute{Computed: true, Description: "Input name: the key to give it a value when ordering."},
					"label":    schema.StringAttribute{Computed: true, Description: "Label shown on the form."},
					"type":     schema.StringAttribute{Computed: true, Description: "Input type, for example `text`, `select` or `checkbox`."},
					"required": schema.BoolAttribute{Computed: true, Description: "Whether an order must give it. See also `required_when`."},
					"default":  schema.StringAttribute{Computed: true, Description: "Value the form pre-fills, or empty."},
					"help":     schema.StringAttribute{Computed: true, Description: "Help text shown on the form, or empty. It often states minimums, for example a disk size."},
					"required_when": schema.StringAttribute{Computed: true, Description: "Required only while another input has a value, for example " +
						"`enableAuth` (while it is on) or `db_engine:mysql`; empty when it doesn't depend on one."},
					"shown_when": schema.StringAttribute{Computed: true, Description: "Shown, and used, only while another input has a value, in the " +
						"same form as `required_when`; empty when always shown."},
					"hidden":   schema.BoolAttribute{Computed: true, Description: "Not shown on the form: the form sends `default` itself."},
					"locked":   schema.BoolAttribute{Computed: true, Description: "Shown but not editable: an order sends `default`."},
					"no_blank": schema.BoolAttribute{Computed: true, Description: "A value given must not be empty."},
					"pattern":  schema.StringAttribute{Computed: true, Description: "Regular expression a typed value must match, or empty."},
					"example":  schema.StringAttribute{Computed: true, Description: "Example value the form shows, or empty."},
					"min":      schema.Int64Attribute{Computed: true, Description: "Smallest number allowed, or null for no limit."},
					"max":      schema.Int64Attribute{Computed: true, Description: "Largest number allowed, or null for no limit."},
				}},
			},
		},
	}
}

func (d *catalogItemDataSource) ConfigValidators(context.Context) []datasource.ConfigValidator {
	return []datasource.ConfigValidator{datasourcevalidator.ExactlyOneOf(path.MatchRoot("id"), path.MatchRoot("name"))}
}

func (d *catalogItemDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	if !d.ready(&resp.Diagnostics) {
		return
	}
	var m oneCatalogItemModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	items, err := d.client.CatalogItems(ctx)
	if err != nil {
		apiError(&resp.Diagnostics, "the catalog", err)
		return
	}
	var found []client.CatalogItem
	for _, it := range items {
		if (!m.ID.IsNull() && it.ID == m.ID.ValueInt64()) || (m.ID.IsNull() && it.Name == m.Name.ValueString()) {
			found = append(found, it)
		}
	}
	switch {
	case len(found) == 0 && !m.ID.IsNull():
		resp.Diagnostics.AddAttributeError(path.Root("id"), "No such catalog item",
			fmt.Sprintf("There is no catalog item %d you can order; mtncloud_catalog_items lists them.", m.ID.ValueInt64()))
		return
	case len(found) == 0:
		names := make([]string, len(items))
		for i, it := range items {
			names[i] = it.Name
		}
		resp.Diagnostics.AddAttributeError(path.Root("name"), "No such catalog item",
			fmt.Sprintf("No catalog item is named %q. Names are matched exactly. You can order: %s.", m.Name.ValueString(), strings.Join(names, ", ")))
		return
	case len(found) > 1:
		ids := make([]string, len(found))
		for i, it := range found {
			ids[i] = strconv.FormatInt(it.ID, 10)
		}
		resp.Diagnostics.AddAttributeError(path.Root("name"), "Several catalog items match",
			fmt.Sprintf("%d catalog items are named %q (IDs %s). Look one up by id.", len(found), m.Name.ValueString(), strings.Join(ids, ", ")))
		return
	}
	it := found[0]
	inputs, err := d.client.CatalogItemInputs(ctx, it.ID)
	if err != nil {
		apiError(&resp.Diagnostics, "the catalog item's inputs", err)
		return
	}
	m = oneCatalogItemModel{
		ID: types.Int64Value(it.ID), Name: types.StringValue(it.Name), Description: types.StringValue(it.Description),
		Type: types.StringValue(it.Type), Featured: types.BoolValue(it.Featured), Inputs: []catalogInputModel{},
	}
	for _, in := range inputs {
		m.Inputs = append(m.Inputs, catalogInputModel{
			Name: types.StringValue(in.Name), Label: types.StringValue(in.Label), Type: types.StringValue(in.Type),
			Required: types.BoolValue(in.Required), Default: types.StringValue(in.Default), Help: types.StringValue(in.Help),
			RequiredWhen: types.StringValue(in.RequireOn), ShownWhen: types.StringValue(in.VisibleOn),
			Hidden: types.BoolValue(in.Hidden), Locked: types.BoolValue(in.Locked), NoBlank: types.BoolValue(in.NoBlank),
			Pattern: types.StringValue(in.Pattern), Example: types.StringValue(in.Placeholder),
			Min: types.Int64PointerValue(in.Min), Max: types.Int64PointerValue(in.Max),
		})
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}
