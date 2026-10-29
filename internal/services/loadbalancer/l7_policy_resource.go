package loadbalancer

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-timeouts/resource/timeouts"
	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/mtn/terraform-provider-mtncloud/internal/client"
)

var (
	_ resource.ResourceWithImportState    = (*l7PolicyResource)(nil)
	_ resource.ResourceWithValidateConfig = (*l7PolicyResource)(nil)
)

func newL7PolicyResource() resource.Resource { return &l7PolicyResource{} }

type l7PolicyResource struct{ rbase }

type l7PolicyRes struct {
	ID               types.String   `tfsdk:"id"`
	NetworkID        types.Int64    `tfsdk:"network_id"`
	LoadBalancerID   types.String   `tfsdk:"load_balancer_id"`
	ListenerID       types.String   `tfsdk:"listener_id"`
	Name             types.String   `tfsdk:"name"`
	Description      types.String   `tfsdk:"description"`
	Action           types.String   `tfsdk:"action"`
	Position         types.Int64    `tfsdk:"position"`
	RedirectPoolID   types.String   `tfsdk:"redirect_pool_id"`
	RedirectURL      types.String   `tfsdk:"redirect_url"`
	RedirectPrefix   types.String   `tfsdk:"redirect_prefix"`
	RedirectHTTPCode types.Int64    `tfsdk:"redirect_http_code"`
	Enabled          types.Bool     `tfsdk:"enabled"`
	Rules            types.Set      `tfsdk:"rules"`
	Timeouts         timeouts.Value `tfsdk:"timeouts"`
}

type l7RuleModel struct {
	Type        types.String `tfsdk:"type"`
	CompareType types.String `tfsdk:"compare_type"`
	Key         types.String `tfsdk:"key"`
	Value       types.String `tfsdk:"value"`
	Invert      types.Bool   `tfsdk:"invert"`
}

var l7RuleType = types.ObjectType{AttrTypes: map[string]attr.Type{
	"type": types.StringType, "compare_type": types.StringType, "key": types.StringType, "value": types.StringType, "invert": types.BoolType,
}}

const l7Kind = "L7 policy"

// The plugin's enums (LbTopology.L7_*).
var (
	l7Actions      = []string{"REDIRECT_TO_POOL", "REDIRECT_TO_URL", "REDIRECT_PREFIX", "REJECT"}
	l7RuleTypes    = []string{"HOST_NAME", "PATH", "FILE_TYPE", "HEADER", "COOKIE", "SSL_CONN_HAS_CERT", "SSL_VERIFY_RESULT", "SSL_DN_FIELD"}
	l7CompareTypes = []string{"REGEX", "STARTS_WITH", "ENDS_WITH", "CONTAINS", "EQUAL_TO"}
	httpURL        = regexp.MustCompile(`(?i)^https?://.+`)
)

func (r *l7PolicyResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_load_balancer_l7_policy"
}

func (r *l7PolicyResource) Schema(ctx context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "A layer 7 policy on an `HTTP` or `TERMINATED_HTTPS` listener: requests that match all its rules are sent to " +
			"another pool, redirected, or rejected. A listener tries its policies in `position` order, and the first that matches " +
			"acts; requests no policy matches go to the listener's default pool. The policy and its rules are created together: " +
			"if a rule is refused, the policy is not left behind.",
		Attributes: map[string]schema.Attribute{
			"id":               idAttr(l7Kind),
			"network_id":       networkAttr(),
			"load_balancer_id": lbIDAttr(),
			"listener_id": schema.StringAttribute{Required: true, PlanModifiers: replaceStr,
				Description: "The listener, for example `mtncloud_load_balancer_listener.web.id`. It must be `HTTP` or `TERMINATED_HTTPS`. " +
					"Changing it replaces the policy."},
			"name":        nameAttr(l7Kind),
			"description": descriptionAttr(),
			"action": schema.StringAttribute{Required: true, Validators: []validator.String{stringvalidator.OneOf(l7Actions...)},
				Description: "What to do with a matching request: `REDIRECT_TO_POOL` (send it to `redirect_pool_id`), `REDIRECT_TO_URL` " +
					"(redirect to `redirect_url`), `REDIRECT_PREFIX` (redirect to `redirect_prefix` plus the request's path) or `REJECT`."},
			"position": schema.Int64Attribute{Required: true, Validators: []validator.Int64{int64validator.AtLeast(1)},
				Description: "Its place in the listener's order, from 1. A policy put at a position another holds goes there, and " +
					"the ones from that position on move down one; a position past the end puts it last. Number a listener's " +
					"policies 1, 2, 3 without gaps: one apply places its new and moved policies in position order, so each " +
					"ends where it is configured. The cloud closes gaps, so after one above it is deleted a policy reports a " +
					"lower position than this, which is the same order and no change."},
			"redirect_pool_id": schema.StringAttribute{Optional: true,
				Description: "For `REDIRECT_TO_POOL`: the pool, for example `mtncloud_load_balancer_pool.api.id`. It must be an `HTTP`, " +
					"`HTTPS`, `PROXY` or `PROXYV2` pool of the same load balancer."},
			"redirect_url": schema.StringAttribute{Optional: true, Validators: []validator.String{stringvalidator.RegexMatches(httpURL, "must start with http:// or https://")},
				Description: "For `REDIRECT_TO_URL`: where to redirect, starting with `http://` or `https://`."},
			"redirect_prefix": schema.StringAttribute{Optional: true, Validators: []validator.String{stringvalidator.RegexMatches(httpURL, "must start with http:// or https://")},
				Description: "For `REDIRECT_PREFIX`: the start of the new address, starting with `http://` or `https://`; the request's path is added to it."},
			// Optional+Computed: the cloud answers 302 for a redirect that doesn't set it.
			"redirect_http_code": schema.Int64Attribute{Optional: true, Computed: true,
				Validators:  []validator.Int64{int64validator.OneOf(301, 302, 303, 307, 308)},
				Description: "For `REDIRECT_TO_URL` and `REDIRECT_PREFIX`: the redirect's status code, `301`, `302`, `303`, `307` or `308`. The cloud uses `302` when it isn't set on create." + keptWhenRemoved},
			"enabled": enabledAttr(l7Kind),
			"rules": schema.SetNestedAttribute{Optional: true,
				Description: "The conditions a request must all meet. Without rules the policy matches every request. Changing the rules " +
					"replaces all of them at once.",
				NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
					"type": schema.StringAttribute{Required: true, Validators: []validator.String{stringvalidator.OneOf(l7RuleTypes...)},
						Description: "What is compared: `HOST_NAME`, `PATH`, `FILE_TYPE`, `HEADER`, `COOKIE`, `SSL_CONN_HAS_CERT`, `SSL_VERIFY_RESULT` or `SSL_DN_FIELD`."},
					"compare_type": schema.StringAttribute{Required: true, Validators: []validator.String{stringvalidator.OneOf(l7CompareTypes...)},
						Description: "How: `EQUAL_TO`, `STARTS_WITH`, `ENDS_WITH`, `CONTAINS` or `REGEX`."},
					"key": schema.StringAttribute{Optional: true, Validators: []validator.String{stringvalidator.LengthAtLeast(1)},
						Description: "The header or cookie name, for `HEADER` and `COOKIE` rules (required there)."},
					"value": schema.StringAttribute{Required: true, Validators: []validator.String{stringvalidator.LengthAtLeast(1)},
						Description: "The value to compare with, for example `/api` or `api.example.com`."},
					// Optional+Computed: false when left out, so a rule read back after import matches either way.
					"invert": schema.BoolAttribute{Optional: true, Computed: true, Default: booldefault.StaticBool(false),
						Description: "Match when the comparison fails instead. Defaults to `false`."},
				}},
			},
		},
		Blocks: map[string]schema.Block{
			"timeouts": timeouts.Block(ctx, timeouts.Opts{Create: true, Update: true, Delete: true}),
		},
	}
}

// ValidateConfig checks what each action needs, as the plugin would, but at plan time.
func (r *l7PolicyResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var m l7PolicyRes
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() || m.Action.IsUnknown() {
		return
	}
	action := m.Action.ValueString()
	needs := map[string]string{"REDIRECT_TO_POOL": "redirect_pool_id", "REDIRECT_TO_URL": "redirect_url", "REDIRECT_PREFIX": "redirect_prefix"}
	for a, attrName := range needs {
		v := map[string]types.String{"redirect_pool_id": m.RedirectPoolID, "redirect_url": m.RedirectURL, "redirect_prefix": m.RedirectPrefix}[attrName]
		switch {
		case a == action && v.IsNull():
			resp.Diagnostics.AddAttributeError(path.Root(attrName), "Missing "+attrName, fmt.Sprintf("action %s needs %s.", action, attrName))
		case a != action && !v.IsNull():
			resp.Diagnostics.AddAttributeError(path.Root(attrName), "Unexpected "+attrName, fmt.Sprintf("%s is only for action %s; this policy's action is %s.", attrName, a, action))
		}
	}
	if !m.RedirectHTTPCode.IsNull() && !m.RedirectHTTPCode.IsUnknown() && action != "REDIRECT_TO_URL" && action != "REDIRECT_PREFIX" {
		resp.Diagnostics.AddAttributeError(path.Root("redirect_http_code"), "Unexpected redirect_http_code",
			"redirect_http_code is only for REDIRECT_TO_URL and REDIRECT_PREFIX.")
	}
	if m.Rules.IsUnknown() {
		return
	}
	var rules []l7RuleModel
	resp.Diagnostics.Append(m.Rules.ElementsAs(ctx, &rules, false)...)
	for _, rule := range rules {
		t := rule.Type.ValueString()
		if (t == "HEADER" || t == "COOKIE") && rule.Key.IsNull() && !rule.Type.IsUnknown() {
			resp.Diagnostics.AddAttributeError(path.Root("rules"), "Missing key", fmt.Sprintf("A %s rule needs key: the %s name.", t, strings.ToLower(t)))
		}
	}
	if len(rules) == 0 && !m.Rules.IsNull() {
		resp.Diagnostics.AddAttributeError(path.Root("rules"), "Empty rules", "Leave rules out for a policy that matches every request, rather than giving an empty set.")
	}
	if action == "REJECT" && m.Rules.IsNull() && !m.Position.IsUnknown() && m.Position.ValueInt64() == 1 {
		resp.Diagnostics.AddAttributeError(path.Root("rules"), "Policy would reject everything",
			"A REJECT policy without rules at position 1 rejects all traffic on the listener. Add a rule, or move it.")
	}
}

func (r *l7PolicyResource) spec(ctx context.Context, m *l7PolicyRes, diags *diag.Diagnostics) client.LBL7PolicySpec {
	s := client.LBL7PolicySpec{
		Name: m.Name.ValueString(), Description: m.Description.ValueString(), Action: m.Action.ValueString(), Position: m.Position.ValueInt64(),
		RedirectPoolID: m.RedirectPoolID.ValueString(), RedirectURL: m.RedirectURL.ValueString(), RedirectPrefix: m.RedirectPrefix.ValueString(),
		Enabled: m.Enabled.ValueBool(),
	}
	if !m.RedirectHTTPCode.IsUnknown() {
		s.RedirectHTTPCode = m.RedirectHTTPCode.ValueInt64()
	}
	if !m.Rules.IsNull() && !m.Rules.IsUnknown() {
		var rules []l7RuleModel
		diags.Append(m.Rules.ElementsAs(ctx, &rules, false)...)
		for _, x := range rules {
			s.Rules = append(s.Rules, client.LBL7RuleSpec{Type: x.Type.ValueString(), CompareType: x.CompareType.ValueString(),
				Key: x.Key.ValueString(), Value: x.Value.ValueString(), Invert: x.Invert.ValueBool()})
		}
	}
	return s
}

// fill reads the policy into m. Gone when the listener or the policy isn't there; an error when the
// listener's policies could not be read, which is not "none".
func (r *l7PolicyResource) fill(m *l7PolicyRes, lb *client.LoadBalancer, diags *diag.Diagnostics) (gone bool) {
	l := client.ListenerOf(lb, m.ListenerID.ValueString())
	if l == nil {
		return true
	}
	if l.L7Error != "" {
		diags.AddError("Unable to read the L7 policy", "The listener's L7 policies could not be read: "+l.L7Error+". Run the command again.")
		return false
	}
	for _, p := range l.L7Policies {
		if p.ID != m.ID.ValueString() {
			continue
		}
		if p.RulesError != "" {
			diags.AddError("Unable to read the L7 policy", "The policy's rules could not be read: "+p.RulesError+". Run the command again.")
			return false
		}
		m.Name, m.Description = types.StringValue(p.Name), types.StringValue(p.Description)
		// The cloud closes gaps: asked for 2 on an empty listener, or after the policy above is
		// deleted, it reports 1. That is the same order, so a lower position keeps the configured one.
		// A policy really moved down reads higher than configured, and one moved up pushes another down.
		if was := m.Position; was.IsNull() || was.IsUnknown() || p.Position > was.ValueInt64() {
			m.Position = types.Int64Value(p.Position)
		}
		m.Action = types.StringValue(p.Action)
		m.RedirectPoolID, m.RedirectURL, m.RedirectPrefix = optStr(p.RedirectPoolID), optStr(p.RedirectURL), optStr(p.RedirectPrefix)
		m.RedirectHTTPCode = types.Int64Null()
		if p.RedirectHTTPCode != 0 {
			m.RedirectHTTPCode = types.Int64Value(p.RedirectHTTPCode)
		}
		m.Enabled = types.BoolValue(p.AdminStateUp)
		m.Rules = flattenRules(p.Rules)
		return false
	}
	return true
}

// flattenRules reads the cloud's rules as the set, keeping an unset key null, not "".
func flattenRules(in []client.LBL7Rule) types.Set {
	if len(in) == 0 {
		return types.SetNull(l7RuleType)
	}
	elems := make([]attr.Value, 0, len(in))
	for _, x := range in {
		elems = append(elems, types.ObjectValueMust(l7RuleType.AttrTypes, map[string]attr.Value{
			"type": types.StringValue(x.Type), "compare_type": types.StringValue(x.CompareType), "key": optStr(x.Key),
			"value": types.StringValue(x.Value), "invert": types.BoolValue(x.Invert),
		}))
	}
	return types.SetValueMust(l7RuleType, elems)
}

func (r *l7PolicyResource) refresh(ctx context.Context, m *l7PolicyRes, st *tfsdk.State, diags *diag.Diagnostics) {
	if lb, _ := r.readLB(ctx, m.NetworkID.ValueInt64(), m.LoadBalancerID.ValueString(), diags); lb != nil && r.fill(m, lb, diags) {
		missingAfterWrite(diags, l7Kind)
	}
	diags.Append(st.Set(ctx, m)...)
}

func (r *l7PolicyResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	if !r.ready(&resp.Diagnostics) {
		return
	}
	var plan l7PolicyRes
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	timeout, d := plan.Timeouts.Create(ctx, lbTimeout)
	resp.Diagnostics.Append(d...)
	s := r.spec(ctx, &plan, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	id, err := r.client.CreateLBL7Policy(ctx, plan.NetworkID.ValueInt64(), plan.LoadBalancerID.ValueString(), plan.ListenerID.ValueString(), s)
	if id != "" {
		plan.ID = types.StringValue(id)
		r.refresh(ctx, &plan, &resp.State, &resp.Diagnostics)
	}
	if err != nil {
		resp.Diagnostics.AddError("Unable to create the L7 policy", err.Error())
	}
}

func (r *l7PolicyResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	if !r.ready(&resp.Diagnostics) {
		return
	}
	var state l7PolicyRes
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	lb, gone := r.readLB(ctx, state.NetworkID.ValueInt64(), state.LoadBalancerID.ValueString(), &resp.Diagnostics)
	if gone || (lb != nil && r.fill(&state, lb, &resp.Diagnostics)) {
		resp.State.RemoveResource(ctx)
		return
	}
	if lb != nil && !resp.Diagnostics.HasError() {
		resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
	}
}

func (r *l7PolicyResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	if !r.ready(&resp.Diagnostics) {
		return
	}
	var plan, state l7PolicyRes
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	timeout, d := plan.Timeouts.Update(ctx, lbTimeout)
	resp.Diagnostics.Append(d...)
	s := r.spec(ctx, &plan, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	plan.ID = state.ID
	if err := r.client.UpdateLBL7Policy(ctx, plan.NetworkID.ValueInt64(), plan.LoadBalancerID.ValueString(), plan.ListenerID.ValueString(), plan.ID.ValueString(), s, !plan.Position.Equal(state.Position)); err != nil {
		resp.Diagnostics.AddError("Unable to change the L7 policy", err.Error())
		return
	}
	r.refresh(ctx, &plan, &resp.State, &resp.Diagnostics)
}

func (r *l7PolicyResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	if !r.ready(&resp.Diagnostics) {
		return
	}
	var state l7PolicyRes
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	timeout, d := state.Timeouts.Delete(ctx, lbTimeout)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if err := r.client.DeleteLBL7Policy(ctx, state.NetworkID.ValueInt64(), state.LoadBalancerID.ValueString(), state.ListenerID.ValueString(), state.ID.ValueString()); err != nil {
		resp.Diagnostics.AddError("Unable to delete the L7 policy", err.Error())
	}
}

func (r *l7PolicyResource) ImportState(_ context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	importIDs(req, resp, "<network_id>/<load_balancer_id>/<listener_id>/<l7_policy_id>", "network_id", "load_balancer_id", "listener_id", "id")
}
