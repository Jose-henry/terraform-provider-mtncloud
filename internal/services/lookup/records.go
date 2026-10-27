package lookup

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/datasourcevalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/mtn/terraform-provider-mtncloud/internal/client"
)

// Most read-only lookups only show what one list endpoint returns. A recordSpec describes one:
// the endpoint and the fields to show. From it come up to two data sources: mtncloud_<one>,
// one object by id or name, and mtncloud_<many>, a list filtered by name_regex. Both read the
// list once (rule 21) and never call a write endpoint. Fields that hold secrets (passwords,
// keys, hashes) are never listed in a spec.

type kind int

const (
	text    kind = iota // string
	number              // int64
	flag                // bool
	texts               // list of strings
	numbers             // list of int64
)

type field struct {
	attr string // attribute name
	src  string // dotted JSON path, for example "zone.name"; through a list it reads every element ("roles.authority")
	kind kind
	desc string
}

type recordSpec struct {
	one, many   string // type names after "mtncloud_"; empty when that half isn't served (an older lookup has the name)
	noun, nouns string // for descriptions and errors: "app", "apps"
	path, key   string // list endpoint and the key of its list, for example "/api/apps", "apps"
	nameAttr    string // attribute holding the name; default "name"
	nameSrc     string // JSON path of the name; default "name"
	about       string // one or two sentences on what the objects are and what to use them for
	fields      []field
}

func (s recordSpec) nameA() string { return cmp.Or(s.nameAttr, "name") }
func (s recordSpec) nameS() string { return cmp.Or(s.nameSrc, "name") }

func (s recordSpec) name(r client.Record) string { return asText(at(r, s.nameS())) }

func (s recordSpec) id(r client.Record) int64 { return asNumber(r["id"]) }

func (f field) schema() schema.Attribute {
	switch f.kind {
	case number:
		return schema.Int64Attribute{Computed: true, Description: f.desc}
	case flag:
		return schema.BoolAttribute{Computed: true, Description: f.desc}
	case texts:
		return schema.ListAttribute{Computed: true, ElementType: types.StringType, Description: f.desc}
	case numbers:
		return schema.ListAttribute{Computed: true, ElementType: types.Int64Type, Description: f.desc}
	default:
		return schema.StringAttribute{Computed: true, Description: f.desc}
	}
}

func (f field) typ() attr.Type {
	switch f.kind {
	case number:
		return types.Int64Type
	case flag:
		return types.BoolType
	case texts:
		return types.ListType{ElemType: types.StringType}
	case numbers:
		return types.ListType{ElemType: types.Int64Type}
	default:
		return types.StringType
	}
}

// value reads the field from r. Missing or null values become the zero value (empty string, 0,
// false, empty list), never null, so a reference to them always works.
func (f field) value(r client.Record) attr.Value {
	v := at(r, f.src)
	switch f.kind {
	case number:
		return types.Int64Value(asNumber(v))
	case flag:
		return types.BoolValue(asFlag(v))
	case texts:
		var out []attr.Value
		for _, x := range asList(v) {
			if s := asText(x); s != "" {
				out = append(out, types.StringValue(s))
			}
		}
		return types.ListValueMust(types.StringType, append([]attr.Value{}, out...))
	case numbers:
		var out []attr.Value
		for _, x := range asList(v) {
			if x != nil {
				out = append(out, types.Int64Value(asNumber(x)))
			}
		}
		return types.ListValueMust(types.Int64Type, append([]attr.Value{}, out...))
	default:
		return types.StringValue(asText(v))
	}
}

func (s recordSpec) attrTypes() map[string]attr.Type {
	t := map[string]attr.Type{"id": types.Int64Type, s.nameA(): types.StringType}
	for _, f := range s.fields {
		t[f.attr] = f.typ()
	}
	return t
}

func (s recordSpec) values(r client.Record) map[string]attr.Value {
	v := map[string]attr.Value{"id": types.Int64Value(s.id(r)), s.nameA(): types.StringValue(s.name(r))}
	for _, f := range s.fields {
		v[f.attr] = f.value(r)
	}
	return v
}

// at follows a dotted path through decoded JSON. On reaching a list it reads the rest of the
// path from every element and returns those values as a list.
func at(v any, dotted string) any {
	if r, ok := v.(client.Record); ok {
		v = map[string]any(r)
	}
	segs := strings.Split(dotted, ".")
	for i, s := range segs {
		switch x := v.(type) {
		case map[string]any:
			v = x[s]
		case []any:
			out := make([]any, 0, len(x))
			rest := strings.Join(segs[i:], ".")
			for _, e := range x {
				out = append(out, at(e, rest))
			}
			return out
		default:
			return nil
		}
	}
	return v
}

func asText(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(x)
	case map[string]any:
		// Some fields are a string on one platform version and {id, name} on another; people
		// ({id, username}) have no name.
		if n, ok := x["name"].(string); ok {
			return n
		}
		if n, ok := x["username"].(string); ok {
			return n
		}
	}
	return ""
}

func asNumber(v any) int64 {
	switch x := v.(type) {
	case float64:
		return int64(x)
	case string:
		n, _ := strconv.ParseInt(x, 10, 64)
		return n
	case map[string]any:
		return asNumber(x["id"])
	}
	return 0
}

func asFlag(v any) bool {
	switch x := v.(type) {
	case bool:
		return x
	case string:
		return x == "true" || x == "on"
	}
	return false
}

func asList(v any) []any {
	if l, ok := v.([]any); ok {
		return l
	}
	return nil
}

func recordDataSources() []func() datasource.DataSource {
	var out []func() datasource.DataSource
	for _, s := range recordSpecs {
		if s.one != "" {
			out = append(out, func() datasource.DataSource { return &recordOne{spec: s} })
		}
		if s.many != "" {
			out = append(out, func() datasource.DataSource { return &recordMany{spec: s} })
		}
	}
	return out
}

// recordOne is mtncloud_<one>: one object by id or name.
type recordOne struct {
	base
	spec recordSpec
}

func (d *recordOne) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_" + d.spec.one
}

func (d *recordOne) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	s := d.spec
	attrs := map[string]schema.Attribute{
		// Optional+Computed: give id or the name, and the other is filled in.
		"id": schema.Int64Attribute{Optional: true, Computed: true, Description: fmt.Sprintf("ID of the %s. Give this or `%s`.", s.noun, s.nameA())},
		s.nameA(): schema.StringAttribute{
			Optional: true, Computed: true,
			Description: fmt.Sprintf("Name of the %s, matched exactly. Give this or `id`.", s.noun),
		},
	}
	for _, f := range s.fields {
		attrs[f.attr] = f.schema()
	}
	resp.Schema = schema.Schema{
		Description: fmt.Sprintf("Looks up one %s by ID or by name, including one made in the console. %s A name must match exactly one %s.",
			s.noun, s.about, s.noun),
		Attributes: attrs,
	}
}

func (d *recordOne) ConfigValidators(context.Context) []datasource.ConfigValidator {
	return []datasource.ConfigValidator{datasourcevalidator.ExactlyOneOf(path.MatchRoot("id"), path.MatchRoot(d.spec.nameA()))}
}

func (d *recordOne) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	if !d.ready(&resp.Diagnostics) {
		return
	}
	s := d.spec
	var id types.Int64
	var name types.String
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("id"), &id)...)
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root(s.nameA()), &name)...)
	if resp.Diagnostics.HasError() {
		return
	}
	all, err := d.client.ListRecords(ctx, s.path, s.key, nil)
	if err != nil {
		apiError(&resp.Diagnostics, s.nouns, err)
		return
	}
	var found []client.Record
	for _, r := range all {
		if (!id.IsNull() && s.id(r) == id.ValueInt64()) || (id.IsNull() && s.name(r) == name.ValueString()) {
			found = append(found, r)
		}
	}
	switch {
	case len(found) == 0 && !id.IsNull():
		resp.Diagnostics.AddAttributeError(path.Root("id"), "No such "+s.noun,
			fmt.Sprintf("There is no %s %d you can see.", s.noun, id.ValueInt64()))
		return
	case len(found) == 0:
		resp.Diagnostics.AddAttributeError(path.Root(s.nameA()), "No such "+s.noun,
			fmt.Sprintf("No %s is named %q. Names are matched exactly. %s", s.noun, name.ValueString(), available(s, all)))
		return
	case len(found) > 1:
		ids := make([]string, len(found))
		for i, r := range found {
			ids[i] = strconv.FormatInt(s.id(r), 10)
		}
		resp.Diagnostics.AddAttributeError(path.Root(s.nameA()), "Several "+s.nouns+" match",
			fmt.Sprintf("%d %s are named %q (IDs %s). Look one up by id.", len(found), s.nouns, name.ValueString(), strings.Join(ids, ", ")))
		return
	}
	for k, v := range s.values(found[0]) {
		resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root(k), v)...)
	}
}

// available names what there is to choose from, so nobody has to open the console.
func available(s recordSpec, all []client.Record) string {
	if len(all) == 0 {
		return fmt.Sprintf("You have no %s.", s.nouns)
	}
	names := make([]string, 0, len(all))
	for _, r := range all {
		names = append(names, s.name(r))
	}
	slices.Sort(names)
	names = slices.Compact(names)
	more := ""
	if len(names) > 20 {
		more = fmt.Sprintf(" and %d more", len(names)-20)
		names = names[:20]
	}
	return fmt.Sprintf("Yours: %s%s.", strings.Join(names, ", "), more)
}

// recordMany is mtncloud_<many>: every object, filtered by name_regex.
type recordMany struct {
	base
	spec recordSpec
}

// Filters on a list: every field the API returns that people narrow a list by. Text fields
// match exactly (case ignored), flags match their value, and label-like lists match when every
// value given is there.
var filterable = map[string]bool{
	"status": true, "app_status": true, "execution_status": true, "type": true, "group": true, "cloud": true,
	"visibility": true, "environment": true, "role_type": true, "provider_type": true, "catalog_item": true,
	"network": true, "datastore": true, "target_type": true, "cert_type": true, "blueprint": true, "layout": true,
	"owner": true, "platform": true, "enabled": true, "active": true, "online": true, "labels": true, "roles": true,
}

func (s recordSpec) filters() []field {
	var out []field
	for _, f := range s.fields {
		if filterable[f.attr] && (f.kind == text || f.kind == flag || f.kind == texts) {
			out = append(out, f)
		}
	}
	return out
}

func (f field) filterSchema(nouns string) schema.Attribute {
	switch f.kind {
	case flag:
		return schema.BoolAttribute{Optional: true, Description: fmt.Sprintf("Only list %s whose `%s` is this.", nouns, f.attr)}
	case texts:
		return schema.ListAttribute{Optional: true, ElementType: types.StringType,
			Description: fmt.Sprintf("Only list %s that have every one of these `%s` (case ignored).", nouns, f.attr)}
	default:
		return schema.StringAttribute{Optional: true, Description: fmt.Sprintf("Only list %s whose `%s` is this (case ignored).", nouns, f.attr)}
	}
}

// keeps reports whether r passes the filter's configured value v (null passes everything).
func (f field) keeps(r client.Record, v attr.Value) bool {
	if v == nil || v.IsNull() || v.IsUnknown() {
		return true
	}
	got := at(r, f.src)
	switch x := v.(type) {
	case types.Bool:
		return asFlag(got) == x.ValueBool()
	case types.List:
		have := map[string]bool{}
		for _, e := range asList(got) {
			have[strings.ToLower(asText(e))] = true
		}
		for _, e := range x.Elements() {
			if s, ok := e.(types.String); ok && !have[strings.ToLower(s.ValueString())] {
				return false
			}
		}
		return true
	case types.String:
		return strings.EqualFold(asText(got), x.ValueString())
	}
	return true
}

func (d *recordMany) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_" + d.spec.many
}

func (d *recordMany) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	s := d.spec
	item := map[string]schema.Attribute{
		"id":      schema.Int64Attribute{Computed: true, Description: fmt.Sprintf("ID of the %s.", s.noun)},
		s.nameA(): schema.StringAttribute{Computed: true, Description: fmt.Sprintf("Name of the %s.", s.noun)},
	}
	for _, f := range s.fields {
		item[f.attr] = f.schema()
	}
	attrs := map[string]schema.Attribute{
		"name_regex": nameRegexAttribute(s.nouns),
		s.many: schema.ListNestedAttribute{
			Computed:     true,
			Description:  fmt.Sprintf("The %s, sorted by name, then ID.", s.nouns),
			NestedObject: schema.NestedAttributeObject{Attributes: item},
		},
	}
	for _, f := range s.filters() {
		attrs[f.attr] = f.filterSchema(s.nouns)
	}
	resp.Schema = schema.Schema{
		Description: fmt.Sprintf("Lists your %s, including those made in the console. %s", s.nouns, s.about),
		Attributes:  attrs,
	}
}

func (d *recordMany) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	if !d.ready(&resp.Diagnostics) {
		return
	}
	s := d.spec
	var re types.String
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("name_regex"), &re)...)
	match, diags := matcher(re)
	resp.Diagnostics.Append(diags...)
	want := map[string]attr.Value{}
	for _, f := range s.filters() {
		var v attr.Value
		resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root(f.attr), &v)...)
		want[f.attr] = v
	}
	if resp.Diagnostics.HasError() {
		return
	}
	all, err := d.client.ListRecords(ctx, s.path, s.key, nil)
	if err != nil {
		apiError(&resp.Diagnostics, s.nouns, err)
		return
	}
	var kept []client.Record
	for _, r := range all {
		ok := match(s.name(r))
		for _, f := range s.filters() {
			ok = ok && f.keeps(r, want[f.attr])
		}
		if ok {
			kept = append(kept, r)
		}
	}
	slices.SortFunc(kept, func(a, b client.Record) int {
		return cmp.Or(cmp.Compare(s.name(a), s.name(b)), cmp.Compare(s.id(a), s.id(b)))
	})
	objType := types.ObjectType{AttrTypes: s.attrTypes()}
	items := make([]attr.Value, 0, len(kept))
	for _, r := range kept {
		o, diags := types.ObjectValue(objType.AttrTypes, s.values(r))
		resp.Diagnostics.Append(diags...)
		items = append(items, o)
	}
	list, diags := types.ListValue(objType, items)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("name_regex"), re)...)
	for name, v := range want {
		if v != nil {
			resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root(name), v)...)
		}
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root(s.many), list)...)
}
