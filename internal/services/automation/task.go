package automation

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/resourcevalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64default"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/mtn/terraform-provider-mtncloud/internal/client"
)

var (
	_ resource.ResourceWithConfigure        = (*taskResource)(nil)
	_ resource.ResourceWithConfigValidators = (*taskResource)(nil)
	_ resource.ResourceWithImportState      = (*taskResource)(nil)
	_ resource.ResourceWithModifyPlan       = (*taskResource)(nil)
)

// taskResource is one task type, driven by its spec.
type taskResource struct {
	base
	spec taskSpec
}

func (r *taskResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_" + r.spec.typeName
}

func (r *taskResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	s := r.spec
	attrs := map[string]schema.Attribute{
		"id": schema.StringAttribute{
			Computed: true, Description: "Task ID.",
			PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
		},
		"name": schema.StringAttribute{Required: true, Description: "Task name."},
		"code": schema.StringAttribute{Optional: true, Description: "Task code, a short unique reference."},
		"labels": schema.SetAttribute{
			Optional: true, ElementType: types.StringType, Description: "Labels.",
		},
		// Optional+Computed with a default, here and below: left out, the platform's default is sent
		// and state shows it, so removing a value resets it.
		"visibility": schema.StringAttribute{
			Optional: true, Computed: true, Default: stringdefault.StaticString("private"),
			Description: "`private` (your tenant only, the default) or `public`.",
			Validators:  []validator.String{stringvalidator.OneOf("private", "public")},
		},
		"retryable": schema.BoolAttribute{
			Optional: true, Computed: true, Default: booldefault.StaticBool(false),
			Description: "Retry the task when it fails. Defaults to false.",
		},
		"retry_count": schema.Int64Attribute{
			Optional: true, Computed: true, Default: int64default.StaticInt64(5),
			Description: "How many times to retry when `retryable` is true. Defaults to 5.",
		},
		"retry_delay_seconds": schema.Int64Attribute{
			Optional: true, Computed: true, Default: int64default.StaticInt64(10),
			Description: "Seconds between retries. Defaults to 10.",
		},
		"allow_custom_config": schema.BoolAttribute{
			Optional: true, Computed: true, Default: booldefault.StaticBool(false),
			Description: "Let whoever runs the task pass custom configuration. Defaults to false.",
		},
	}
	if len(s.targets) > 1 {
		attrs["execute_target"] = schema.StringAttribute{
			Optional: true, Computed: true, Default: stringdefault.StaticString(s.targets[0]),
			Description: fmt.Sprintf("Where the task runs: `resource` (on the instance a workflow runs it against) or `local` (on the platform). Defaults to `%s`.", s.targets[0]),
			Validators:  []validator.String{stringvalidator.OneOf(s.targets...)},
		}
	}
	if s.results {
		attrs["result_type"] = schema.StringAttribute{
			Optional:    true,
			Description: "How to read the task's output for later tasks: `value`, `keyValue` or `json`. Leave it out to ignore it.",
			Validators:  []validator.String{stringvalidator.OneOf("value", "keyValue", "json")},
		}
	}
	if s.credential {
		attrs["credential"] = schema.StringAttribute{
			Optional:    true,
			Description: "A stored credential to authenticate with, by name (`mtncloud_credentials` lists them), instead of `user` and `password`.",
		}
	}
	for _, o := range s.options {
		attrs[o.attr] = o.schema()
	}
	resp.Schema = schema.Schema{
		Description: fmt.Sprintf("A %s task (console: Library > Automation > Tasks). %s", s.title, s.about),
		Attributes:  attrs,
	}
}

func (o taskOption) schema() schema.Attribute {
	switch o.kind {
	case flag:
		return schema.BoolAttribute{Optional: true, Computed: true, Default: booldefault.StaticBool(false), Description: o.desc + " Defaults to false."}
	case headers:
		return schema.MapAttribute{Optional: true, ElementType: types.StringType, Description: o.desc}
	case secret:
		return schema.StringAttribute{Optional: !o.required, Required: o.required, Sensitive: true, Description: o.desc}
	}
	if o.platform {
		// Optional+Computed: left out, the platform picks it, and state shows its choice.
		return schema.StringAttribute{Optional: true, Computed: true, Description: o.desc,
			PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}}
	}
	return schema.StringAttribute{Optional: !o.required, Required: o.required, Description: o.desc}
}

// ConfigValidators: a stored credential replaces the task's own user and password.
func (r *taskResource) ConfigValidators(context.Context) []resource.ConfigValidator {
	if !r.spec.credential {
		return nil
	}
	return []resource.ConfigValidator{
		resourcevalidator.Conflicting(path.MatchRoot("credential"), path.MatchRoot("user")),
		resourcevalidator.Conflicting(path.MatchRoot("credential"), path.MatchRoot("password")),
	}
}

func (r *taskResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	if _, err := strconv.ParseInt(req.ID, 10, 64); err != nil {
		resp.Diagnostics.AddError("Invalid import ID", fmt.Sprintf("Import a task by its numeric ID (the `mtncloud_tasks` data source lists them), not %q.", req.ID))
		return
	}
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}

// ModifyPlan checks at plan time (rule 17) that the names given to ref and workflow options exist.
func (r *taskResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.Plan.Raw.IsNull() || r.client == nil {
		return
	}
	for _, o := range r.spec.options {
		if o.kind != ref && o.kind != workflow {
			continue
		}
		var v types.String
		resp.Diagnostics.Append(req.Plan.GetAttribute(ctx, path.Root(o.attr), &v)...)
		if v.IsNull() || v.IsUnknown() {
			continue
		}
		if _, err := r.client.OptionByName(ctx, o.source, o.what, v.ValueString()); err != nil {
			resp.Diagnostics.AddAttributeError(path.Root(o.attr), "Unknown "+o.what, err.Error())
		}
	}
	if r.spec.credential {
		var v types.String
		resp.Diagnostics.Append(req.Plan.GetAttribute(ctx, path.Root("credential"), &v)...)
		if !v.IsNull() && !v.IsUnknown() {
			if _, err := r.client.OptionByName(ctx, "credentials", "credential", v.ValueString()); err != nil {
				resp.Diagnostics.AddAttributeError(path.Root("credential"), "Unknown credential", err.Error())
			}
		}
	}
}

// getter reads one attribute from a plan, config or state.
type getter interface {
	GetAttribute(ctx context.Context, p path.Path, target any) diag.Diagnostics
}

// input builds the request body from the plan, resolving names to IDs.
func (r *taskResource) input(ctx context.Context, g getter, diags *diag.Diagnostics) client.TaskInput {
	str := func(name string) string {
		var v types.String
		diags.Append(g.GetAttribute(ctx, path.Root(name), &v)...)
		return v.ValueString()
	}
	boolean := func(name string) bool {
		var v types.Bool
		diags.Append(g.GetAttribute(ctx, path.Root(name), &v)...)
		return v.ValueBool()
	}
	number := func(name string) int64 {
		var v types.Int64
		diags.Append(g.GetAttribute(ctx, path.Root(name), &v)...)
		return v.ValueInt64()
	}
	var labels []string
	diags.Append(g.GetAttribute(ctx, path.Root("labels"), &labels)...)
	in := client.TaskInput{
		Name: str("name"), Code: str("code"), TypeCode: r.spec.code, Labels: labels, Visibility: str("visibility"),
		ExecuteTarget: r.spec.targets[0], Retryable: boolean("retryable"), RetryCount: number("retry_count"),
		RetryDelaySeconds: number("retry_delay_seconds"), AllowCustomConfig: boolean("allow_custom_config"),
		Options: map[string]any{}, Config: map[string]any{},
	}
	if len(r.spec.targets) > 1 {
		in.ExecuteTarget = str("execute_target")
	}
	if r.spec.results {
		in.ResultType = str("result_type")
	}
	if r.spec.credential {
		if name := str("credential"); name != "" {
			c, err := r.client.OptionByName(ctx, "credentials", "credential", name)
			if err != nil {
				diags.AddAttributeError(path.Root("credential"), "Unknown credential", err.Error())
			}
			in.CredentialID = c.ID
		}
	}
	for _, o := range r.spec.options {
		dst := in.Options
		if o.config {
			dst = in.Config
		}
		// Every option is sent, nil when unset, so an update clears what was removed.
		if o.kind == workflow {
			dst[o.key+"Name"], dst[o.key+"Id"] = nil, nil
		} else {
			dst[o.key] = nil
		}
		switch o.kind {
		case flag:
			if boolean(o.attr) {
				dst[o.key] = "on"
			}
		case headers:
			var m map[string]string
			diags.Append(g.GetAttribute(ctx, path.Root(o.attr), &m)...)
			if len(m) > 0 {
				b, _ := json.Marshal(m) //nolint:errchkjson // a map of strings always marshals
				dst[o.key] = string(b)
			}
		case ref, workflow:
			name := str(o.attr)
			if name == "" {
				continue
			}
			opt, err := r.client.OptionByName(ctx, o.source, o.what, name)
			if err != nil {
				diags.AddAttributeError(path.Root(o.attr), "Unknown "+o.what, err.Error())
				continue
			}
			if o.kind == ref {
				dst[o.key] = strconv.FormatInt(opt.ID, 10)
			} else {
				dst[o.key+"Name"], dst[o.key+"Id"] = opt.Name, strconv.FormatInt(opt.ID, 10)
			}
		default:
			if v := str(o.attr); v != "" {
				dst[o.key] = v
			}
		}
	}
	return in
}

func (r *taskResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	if !r.ready(&resp.Diagnostics) {
		return
	}
	in := r.input(ctx, req.Plan, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	// Idempotent create (rule 11): the API happily makes a second task with the same name.
	existing, err := r.client.FindTasksNamed(ctx, in.Name, in.TypeCode)
	if err != nil {
		apiError(&resp.Diagnostics, "check for an existing task", err)
		return
	}
	if len(existing) > 0 {
		resp.Diagnostics.AddAttributeError(path.Root("name"), "Task already exists",
			fmt.Sprintf("A %s task named %q already exists (ID %d). Import it with `terraform import <address> %d`, or choose another name.",
				r.spec.title, in.Name, existing[0].ID, existing[0].ID))
		return
	}
	t, err := r.client.CreateTask(ctx, in)
	if err != nil {
		apiError(&resp.Diagnostics, "create the task", err)
		return
	}
	resp.State = tfsdk.State{Schema: req.Plan.Schema, Raw: req.Plan.Raw}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), strconv.FormatInt(t.ID, 10))...)
	r.read(ctx, t, &resp.State, &resp.Diagnostics)
}

func (r *taskResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	if !r.ready(&resp.Diagnostics) {
		return
	}
	id, ok := r.id(ctx, req.State, &resp.Diagnostics)
	if !ok {
		return
	}
	t, err := r.client.GetTask(ctx, id)
	if client.IsNotFound(err) {
		resp.State.RemoveResource(ctx) // rule 8: gone, so plan a new one
		return
	}
	if err != nil {
		apiError(&resp.Diagnostics, "read the task", err)
		return
	}
	if t.TypeCode != r.spec.code {
		resp.Diagnostics.AddError("Wrong task type",
			fmt.Sprintf("Task %d is a %q task, not %s. Import it with the resource for its type.", id, t.TypeCode, r.spec.title))
		return
	}
	r.read(ctx, t, &resp.State, &resp.Diagnostics)
}

func (r *taskResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	if !r.ready(&resp.Diagnostics) {
		return
	}
	id, ok := r.id(ctx, req.State, &resp.Diagnostics)
	if !ok {
		return
	}
	in := r.input(ctx, req.Plan, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	t, err := r.client.UpdateTask(ctx, id, in)
	if err != nil {
		apiError(&resp.Diagnostics, "update the task", err)
		return
	}
	resp.State = tfsdk.State{Schema: req.Plan.Schema, Raw: req.Plan.Raw}
	r.read(ctx, t, &resp.State, &resp.Diagnostics)
}

func (r *taskResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	if !r.ready(&resp.Diagnostics) {
		return
	}
	id, ok := r.id(ctx, req.State, &resp.Diagnostics)
	if !ok {
		return
	}
	if err := r.client.DeleteTask(ctx, id); err != nil {
		apiError(&resp.Diagnostics, "delete the task", err)
	}
}

func (r *taskResource) id(ctx context.Context, s tfsdk.State, diags *diag.Diagnostics) (int64, bool) {
	var v types.String
	diags.Append(s.GetAttribute(ctx, path.Root("id"), &v)...)
	id, err := strconv.ParseInt(v.ValueString(), 10, 64)
	if err != nil {
		diags.AddError("Invalid task ID", fmt.Sprintf("The task ID in state, %q, isn't a number. This is a provider bug.", v.ValueString()))
		return 0, false
	}
	return id, true
}

// read writes what the API returned over state. Values that name the same thing as state (case,
// an option's name) keep state's spelling; secrets keep state, as the API doesn't return them.
func (r *taskResource) read(ctx context.Context, t *client.Task, st *tfsdk.State, diags *diag.Diagnostics) {
	set := func(name string, v any) { diags.Append(st.SetAttribute(ctx, path.Root(name), v)...) }
	prior := func(name string) types.String {
		var v types.String
		diags.Append(st.GetAttribute(ctx, path.Root(name), &v)...)
		return v
	}
	// optional strings: empty from the API is null in state, unless state had "" (never set by us).
	optional := func(name, got string) {
		if got == "" && prior(name).IsNull() {
			set(name, types.StringNull())
			return
		}
		set(name, types.StringValue(got))
	}
	set("name", t.Name)
	optional("code", t.Code)
	// An empty set stays empty (labels = []); otherwise none is null.
	var labels types.Set
	diags.Append(st.GetAttribute(ctx, path.Root("labels"), &labels)...)
	if len(t.Labels) > 0 || labels.IsNull() || labels.IsUnknown() {
		labels = types.SetNull(types.StringType)
	}
	if len(t.Labels) > 0 {
		elems := make([]attr.Value, len(t.Labels))
		for i, l := range t.Labels {
			elems[i] = types.StringValue(l)
		}
		labels = types.SetValueMust(types.StringType, elems)
	}
	set("labels", labels)
	if t.Visibility != "" {
		set("visibility", t.Visibility)
	}
	set("retryable", t.Retryable)
	set("retry_count", t.RetryCount)
	set("retry_delay_seconds", t.RetryDelaySeconds)
	set("allow_custom_config", t.AllowCustomConfig)
	if len(r.spec.targets) > 1 && t.ExecuteTarget != "" {
		set("execute_target", t.ExecuteTarget)
	}
	if r.spec.results {
		optional("result_type", t.ResultType)
	}
	if r.spec.credential {
		r.readRef(ctx, "credential", "credentials", t.CredentialID, prior("credential"), set)
	}
	for _, o := range r.spec.options {
		src := t.Options
		if o.config {
			src = t.Config
		}
		raw := src[o.key]
		switch o.kind {
		case secret:
			// not returned: state keeps what was configured
		case flag:
			set(o.attr, isOn(raw))
		case headers:
			m := parseHeaders(raw)
			var v types.Map
			diags.Append(st.GetAttribute(ctx, path.Root(o.attr), &v)...)
			if len(m) == 0 && v.IsNull() {
				continue
			}
			mv, d := types.MapValueFrom(ctx, types.StringType, m)
			diags.Append(d...)
			set(o.attr, mv)
		case ref:
			id, _ := strconv.ParseInt(asString(raw), 10, 64)
			r.readRef(ctx, o.attr, o.source, id, prior(o.attr), set)
		case workflow:
			got := asString(src[o.key+"Name"])
			if p := prior(o.attr); !p.IsNull() && strings.EqualFold(p.ValueString(), got) {
				continue
			}
			optional(o.attr, got)
		default:
			if o.platform {
				set(o.attr, asString(raw))
				continue
			}
			optional(o.attr, asString(raw))
		}
	}
}

// readRef shows an option by name: the prior spelling while it still names the same ID.
func (r *taskResource) readRef(ctx context.Context, attrName, source string, id int64, prior types.String, set func(string, any)) {
	if id == 0 {
		if !prior.IsNull() {
			set(attrName, types.StringNull())
		}
		return
	}
	if !prior.IsNull() {
		if o, err := r.client.OptionByName(ctx, source, attrName, prior.ValueString()); err == nil && o.ID == id {
			return
		}
	}
	if name, ok := r.client.OptionName(ctx, source, id); ok {
		set(attrName, name)
	}
}

func asString(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	}
	return fmt.Sprint(v)
}

func isOn(v any) bool {
	switch x := v.(type) {
	case bool:
		return x
	case string:
		return x == "on" || strings.EqualFold(x, "true")
	}
	return false
}

// parseHeaders reads a key-value input: a JSON object string, or a JSON array of {name, value}
// (the platform's own key-value shape), or the same as decoded JSON.
func parseHeaders(v any) map[string]string {
	if s, ok := v.(string); ok {
		if strings.TrimSpace(s) == "" {
			return nil
		}
		if err := json.Unmarshal([]byte(s), &v); err != nil {
			return nil
		}
	}
	out := map[string]string{}
	switch x := v.(type) {
	case map[string]any:
		for k, val := range x {
			out[k] = asString(val)
		}
	case []any:
		for _, e := range x {
			if m, ok := e.(map[string]any); ok {
				k := asString(m["name"])
				if k == "" {
					k = asString(m["key"])
				}
				out[k] = asString(m["value"])
			}
		}
	}
	return out
}
