package catalog

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/mtn/terraform-provider-mtncloud/internal/client"
)

// MTN changes its order forms without notice (names, fields, help text, 2026-10-30), so the live
// form decides: what it requires, what it hides and pre-fills, and when a field applies. The spec
// only names the arguments and the limits the form states in words.

// formPath is the argument a form field is set by, or extra_inputs when no argument covers it.
func (r *orderResource) formPath(key string) path.Path {
	for _, f := range r.spec.fields {
		if f.key != key {
			continue
		}
		if f.kind == kVolumes {
			return path.Root(f.attr + "root_volume_size")
		}
		return path.Root(f.attr)
	}
	return path.Root("extra_inputs")
}

// applyForm completes and checks an order's values against its live form. atPlan is true at plan
// time: the checks that only report run then, so they show once.
func (r *orderResource) applyForm(ctx context.Context, form *client.CatalogForm, p source, out map[string]any,
	planNames map[string]string, atPlan bool, diags *diag.Diagnostics) {
	r.extraInputs(ctx, form, p, out, diags)

	// What the console sends without asking: a hidden field's default, and a pre-filled value the
	// configuration doesn't set. A name template ("${userInitials}...") is the console's own.
	for key, ff := range form.Fields {
		if _, set := out[key]; set || ff.Default == nil || !conditionMet(ff.VisibleOn, out) {
			continue
		}
		if s, ok := ff.Default.(string); ok && strings.Contains(s, "${") {
			continue
		}
		switch {
		case ff.Hidden:
			out[key] = defaultValue(ff.Default)
		case scalarField(ff.Type):
			out[key] = ff.Default
		}
	}

	// A field shown only for another field's value isn't sent otherwise, as in the console.
	for key, ff := range form.Fields {
		if _, set := out[key]; !set || ff.VisibleOn == "" || conditionMet(ff.VisibleOn, out) {
			continue
		}
		delete(out, key)
		if atPlan {
			diags.AddAttributeWarning(r.formPath(key), "Input not used",
				fmt.Sprintf("The %s form uses %s only when %s, so it isn't sent.", r.spec.item, label(ff), shownCondition(ff.VisibleOn)))
		}
	}
	if !atPlan {
		return
	}

	keys := make([]string, 0, len(form.Fields))
	for k := range form.Fields {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, key := range keys {
		ff := form.Fields[key]
		if _, set := out[key]; set || ff.Hidden || !conditionMet(ff.VisibleOn, out) {
			continue
		}
		required := ff.Required || (ff.RequireOn != "" && conditionMet(ff.RequireOn, out))
		if !required {
			continue
		}
		msg := fmt.Sprintf("The %s order form requires %s", r.spec.item, label(ff))
		if ff.RequireOn != "" {
			msg += " when " + shownCondition(ff.RequireOn)
		}
		if p := r.formPath(key); p.Equal(path.Root("extra_inputs")) {
			msg += fmt.Sprintf(". This resource has no argument for it yet: set it in extra_inputs, as %q", key)
		}
		diags.AddAttributeError(r.formPath(key), "Missing required input", msg+"."+helpOf(ff))
	}
	r.checkRules(form, out, planNames, diags)
}

// extraInputs adds extra_inputs to the order: each a field of the form, a choice by name or value.
func (r *orderResource) extraInputs(ctx context.Context, form *client.CatalogForm, p source, out map[string]any, diags *diag.Diagnostics) {
	if r.spec.target {
		return
	}
	var m types.Map
	diags.Append(p.GetAttribute(ctx, path.Root("extra_inputs"), &m)...)
	if m.IsNull() || m.IsUnknown() {
		return
	}
	extra := map[string]string{}
	for k, v := range m.Elements() {
		s, ok := v.(types.String)
		if !ok || s.IsUnknown() {
			return // known at apply
		}
		extra[k] = s.ValueString()
	}
	known := map[string]bool{}
	for _, f := range r.spec.fields {
		known[f.key] = true
	}
	at := path.Root("extra_inputs")
	for key, v := range extra {
		ff, ok := form.Fields[key]
		switch {
		case !ok:
			diags.AddAttributeError(at, "Unknown form field",
				fmt.Sprintf("The %s order form has no field %q. Its fields: %s.", r.spec.item, key, strings.Join(fieldNames(form), ", ")))
			continue
		case known[key]:
			diags.AddAttributeError(at, "Input has an argument",
				fmt.Sprintf("Set %q with the %s argument, not extra_inputs.", key, r.formPath(key)))
			continue
		}
		switch ff.Type {
		case "select", "radio", "typeahead":
			c, err := r.client.FormChoice(ctx, ff, label(ff), v)
			if err != nil {
				diags.AddAttributeError(at, "Invalid "+key, err.Error())
				continue
			}
			out[key] = c.Value
		case "checkbox":
			b, err := strconv.ParseBool(v)
			if err != nil {
				diags.AddAttributeError(at, "Invalid "+key, fmt.Sprintf("%q is a checkbox: give true or false.", key))
				continue
			}
			out[key] = b
		case "number":
			n, err := strconv.ParseInt(v, 10, 64)
			if err != nil {
				diags.AddAttributeError(at, "Invalid "+key, fmt.Sprintf("%q is a number.", key))
				continue
			}
			out[key] = n
		default:
			out[key] = v
		}
	}
}

// checkRules checks the limits a form states only in its help text.
func (r *orderResource) checkRules(form *client.CatalogForm, out map[string]any, planNames map[string]string, diags *diag.Diagnostics) {
	rl := r.spec.rules
	help := func(key string) string { return helpOf(form.Fields[key]) }
	if _, set := out["volumes"]; !set && rl.minRootGB > 0 {
		diags.AddAttributeError(r.formPath("volumes"), "Missing root disk size",
			fmt.Sprintf("%s needs a root disk of at least %d GB: set root_volume_size. The form's own default is "+
				"smaller.%s", r.spec.item, rl.minRootGB, help("volumes")))
	}
	if vols, ok := out["volumes"].([]any); ok && rl.minRootGB > 0 && len(vols) > 0 {
		if v, ok := vols[0].(map[string]any); ok {
			if n, ok := v["size"].(int64); ok && n < rl.minRootGB {
				diags.AddAttributeError(r.formPath("volumes"), "Root disk too small",
					fmt.Sprintf("%s needs a root disk of at least %d GB; %d GB is set.%s", r.spec.item, rl.minRootGB, n, help("volumes")))
			}
		}
	}
	if nets, ok := out["networks"].([]any); ok {
		switch n := int64(len(nets)); {
		case rl.minNetworks > 0 && n < rl.minNetworks:
			diags.AddAttributeError(r.formPath("networks"), "Too few networks",
				fmt.Sprintf("%s needs at least %d networks; %d is set.%s", r.spec.item, rl.minNetworks, n, help("networks")))
		case rl.maxNetworks > 0 && n > rl.maxNetworks:
			diags.AddAttributeError(r.formPath("networks"), "Too many networks",
				fmt.Sprintf("%s takes at most %d networks; %d are set.%s", r.spec.item, rl.maxNetworks, n, help("networks")))
		}
	}
	for attr, name := range planNames {
		cpu, mem, ok := planSize(name)
		if !ok || (cpu >= rl.minCPU && mem >= rl.minMemoryGB) {
			continue
		}
		diags.AddAttributeError(path.Root(attr), "Plan too small",
			fmt.Sprintf("%s needs at least %d vCPU and %d GB of memory; plan %q has %d vCPU and %d GB.%s",
				r.spec.item, rl.minCPU, rl.minMemoryGB, name, cpu, mem, help("plan")))
	}
	if n, ok := out["no_nodes"].(int64); ok && slices.Contains(rl.badNodes, n) {
		diags.AddAttributeError(r.formPath("no_nodes"), "Node count not supported",
			fmt.Sprintf("%s doesn't support %d nodes.%s", r.spec.item, n, help("no_nodes")))
	}
}

// replaceFailed plans a replace when an instance the order made has failed: the next apply deletes
// it with the order and orders again.
func (r *orderResource) replaceFailed(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	var statuses map[string]string
	resp.Diagnostics.Append(req.State.GetAttribute(ctx, path.Root("instance_statuses"), &statuses)...)
	var failed []string
	for id, st := range statuses {
		if client.InstanceFailed(st) {
			failed = append(failed, id)
		}
	}
	if len(failed) == 0 {
		return
	}
	slices.Sort(failed)
	at := path.Root("instance_statuses")
	resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, at, types.MapUnknown(types.StringType))...)
	resp.RequiresReplace.Append(at)
	resp.Diagnostics.AddAttributeWarning(at, "Failed instance: the order is placed again",
		fmt.Sprintf("Instance %s from this %s order has failed. The plan deletes the order with what it made, and orders it again.",
			strings.Join(failed, ", "), r.spec.item))
}

// conditionMet reports whether a form condition holds for the values so far: "enableAuth" (that
// field is on), "db_engine:mysql" (that field has that value); several, separated by "|" or ",",
// mean any. An empty condition always holds.
func conditionMet(cond string, out map[string]any) bool {
	if strings.TrimSpace(cond) == "" {
		return true
	}
	for _, c := range strings.FieldsFunc(cond, func(r rune) bool { return r == '|' || r == ',' }) {
		key, want, hasValue := strings.Cut(strings.TrimSpace(c), ":")
		v, set := out[key]
		if !set {
			continue
		}
		got := strings.Trim(fmt.Sprint(v), "'")
		if hasValue {
			if strings.EqualFold(got, strings.Trim(want, "^$'")) {
				return true
			}
			continue
		}
		switch strings.ToLower(got) {
		case "", "false", "off", "no", "0":
		default:
			return true
		}
	}
	return false
}

func shownCondition(cond string) string {
	key, want, hasValue := strings.Cut(cond, ":")
	if hasValue {
		return fmt.Sprintf("%s is %q", key, want)
	}
	return key + " is on"
}

// defaultValue is a hidden field's default as the console sends it: a disk or network list is JSON
// text in the form.
func defaultValue(v any) any {
	s, ok := v.(string)
	if !ok || (!strings.HasPrefix(strings.TrimSpace(s), "[") && !strings.HasPrefix(strings.TrimSpace(s), "{")) {
		return v
	}
	var parsed any
	if json.Unmarshal([]byte(s), &parsed) != nil {
		return v
	}
	return parsed
}

// scalarField is a widget whose pre-filled value the console sends as it is.
func scalarField(t string) bool {
	switch t {
	case "text", "number", "select", "checkbox", "textarea", "byteSize", "radio":
		return true
	}
	return false
}

func label(ff client.FormField) string {
	if ff.Label != "" {
		return fmt.Sprintf("%q (%s)", ff.Label, ff.Name)
	}
	return fmt.Sprintf("%q", ff.Name)
}

func helpOf(ff client.FormField) string {
	if h := strings.TrimSpace(ff.Help); h != "" {
		return " The form says: " + strings.Join(strings.Fields(h), " ")
	}
	return ""
}

func fieldNames(form *client.CatalogForm) []string {
	out := make([]string, 0, len(form.Fields))
	for k := range form.Fields {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

// sizeCode is a plan's size code, alone or in brackets ("Small (G2S4)"): G<vCPU>S<memory GB>, or
// for the larger plans Ge<vCPU> with S, M or L before the memory ("Ge32M128"), as the forms list them.
var sizeCode = regexp.MustCompile(`(?i)\bGe?(\d+)[SML](\d+)\b`)

func planSize(name string) (cpu, mem int64, ok bool) {
	m := sizeCode.FindStringSubmatch(name)
	if m == nil {
		return 0, 0, false
	}
	cpu, _ = strconv.ParseInt(m[1], 10, 64)
	mem, _ = strconv.ParseInt(m[2], 10, 64)
	return cpu, mem, true
}
