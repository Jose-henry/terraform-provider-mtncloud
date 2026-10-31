package catalog

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
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

	// A list that can't be blank opens on its first choice in the console, which sends it: MTN's
	// workflows fail without it (msg_engine, kafkaVersion: QA, 2026-10-31). Two passes, so a list
	// shown only for another list's choice (rabbitmqVersion for msg_engine) is filled too.
	for range 2 {
		for _, key := range sortedKeys(form.Fields) {
			ff := form.Fields[key]
			if _, set := out[key]; set || !ff.NoBlank || ff.Hidden || ff.Default != nil || ff.ID == 0 ||
				(ff.Type != "select" && ff.Type != "radio") || !conditionMet(ff.VisibleOn, out) {
				continue
			}
			opts, err := r.client.FormOptionsIn(ctx, ff.ID, formValues(form, out))
			if err != nil {
				diags.AddAttributeError(r.formPath(key), "Cannot read the form's choices",
					fmt.Sprintf("Reading the choices of %s on the %s form failed: %s. Set it to skip the lookup.", label(ff), r.spec.item, err))
				return
			}
			if len(opts) > 0 {
				out[key] = opts[0].Value
			}
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

	keys := sortedKeys(form.Fields)
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
	for _, key := range keys {
		if v, set := out[key]; set {
			r.checkField(form.Fields[key], v, diags)
		}
	}
	r.checkRules(form, out, planNames, diags)
}

// formValues are the form values a list may be built for, as the console sends them: the item, its
// cloud, group and project.
func formValues(form *client.CatalogForm, out map[string]any) url.Values {
	fv := url.Values{"catalogItemTypeId": {strconv.FormatInt(form.ItemID, 10)}}
	for k, x := range out {
		if s, isStr := x.(string); isStr && (k == "cloud" || k == "resourcepool" || strings.HasPrefix(k, "group")) {
			fv.Set(k, s)
		}
	}
	if pool, ok := out["resourcepool"].(string); ok {
		fv.Set("config.customOptions.resourcepool", pool)
	}
	return fv
}

// sortedKeys is the form's field names in order, so checks and choices run the same way each plan.
func sortedKeys(fields map[string]client.FormField) []string {
	keys := make([]string, 0, len(fields))
	for k := range fields {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

// checkField checks a value against the console's own rules for the field: locked, not blank, its
// pattern and its number limits. The order would be refused, or the workflow fail, otherwise.
func (r *orderResource) checkField(ff client.FormField, v any, diags *diag.Diagnostics) {
	at, what := r.formPath(ff.Name), label(ff)
	// The name is locked on some forms (the console names the VM itself); the order still sends it,
	// as orders are found by it.
	// A hidden field, or a disk or network list, is what the form itself fills (PfSense's fixed
	// disks, QA 2026-10-31): only a plain value a user gives is compared.
	if ff.Locked && ff.Name != "name" && !ff.Hidden && scalarField(ff.Type) && ff.Default != nil && !sameValue(v, ff.Default) {
		diags.AddAttributeError(at, "Input locked by the form",
			fmt.Sprintf("The %s form locks %s at %v: it can't be changed. Leave it out.%s", r.spec.item, what, ff.Default, helpOf(ff)))
		return
	}
	switch x := v.(type) {
	case string:
		if ff.NoBlank && strings.TrimSpace(x) == "" {
			diags.AddAttributeError(at, "Empty input", fmt.Sprintf("The %s form needs a value for %s.%s", r.spec.item, what, helpOf(ff)))
			return
		}
		if ff.Pattern == "" || x == "" {
			return
		}
		re, err := regexp.Compile(ff.Pattern)
		if err != nil {
			return // a pattern Go can't run (look-ahead): the console checks it
		}
		if !re.MatchString(x) {
			example := ""
			if ff.Placeholder != "" && ff.Type != "password" {
				example = fmt.Sprintf(" For example %q.", ff.Placeholder)
			}
			msg := fmt.Sprintf("The %s form doesn't accept %s for %s: it must match %s.%s%s", r.spec.item, shownValue(ff, x), what, ff.Pattern, example, helpOf(ff))
			diags.AddAttributeError(at, "Invalid "+ff.Name, msg)
		}
	case int64, float64:
		n := toInt(x)
		if ff.Min != nil && n < *ff.Min || ff.Max != nil && n > *ff.Max {
			diags.AddAttributeError(at, "Out of range",
				fmt.Sprintf("The %s form takes %s from %s to %s; %d is set.%s", r.spec.item, what, bound(ff.Min, "any"), bound(ff.Max, "any"), n, helpOf(ff)))
		}
	}
}

// shownValue hides a secret in an error.
func shownValue(ff client.FormField, v string) string {
	if ff.Type == "password" {
		return "that password"
	}
	return fmt.Sprintf("%q", v)
}

func toInt(v any) int64 {
	switch n := v.(type) {
	case int64:
		return n
	case float64:
		return int64(n)
	}
	return 0
}

func bound(n *int64, none string) string {
	if n == nil {
		return none
	}
	return strconv.FormatInt(*n, 10)
}

// sameValue compares a value with a form default, which the form may give as text ("true", "1").
// A checkbox is sent as "on"/"off", and its default may be "true": those match.
func sameValue(v, def any) bool {
	return sameWord(fmt.Sprint(v), strings.Trim(fmt.Sprint(def), "'"))
}

// sameWord compares two form values, case ignored, with on/true/yes/1 and off/false/no/0 alike.
func sameWord(a, b string) bool {
	return strings.EqualFold(checkboxWord(a), checkboxWord(b))
}

func checkboxWord(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "on", "true", "yes", "1":
		return "on"
	case "off", "false", "no", "0":
		return "off"
	}
	return s
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
	var status types.String
	resp.Diagnostics.Append(req.State.GetAttribute(ctx, path.Root("status"), &status)...)
	if orderFailed(status.ValueString()) && len(statuses) > 0 {
		// The order itself failed but left instances (kept by Read): delete them and order again.
		at := path.Root("status")
		resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, at, types.StringUnknown())...)
		resp.RequiresReplace.Append(at)
		resp.Diagnostics.AddAttributeWarning(at, "Failed order: placed again",
			fmt.Sprintf("This %s order ended with %q. The plan deletes what it left and orders it again.", r.spec.item, status.ValueString()))
		return
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
			if sameWord(got, strings.Trim(want, "^$'")) {
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

// orderFailed reports whether an order's status (its workflow's, lower case) is a failure.
func orderFailed(status string) bool {
	switch status {
	case "failed", "error", "cancelled", "canceled": //nolint:misspell // both spellings come back from the API
		return true
	}
	return false
}
