package network

import (
	"context"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/mtn/terraform-provider-mtncloud/internal/client"
)

var (
	_ resource.ResourceWithConfigure      = (*securityGroupRuleResource)(nil)
	_ resource.ResourceWithImportState    = (*securityGroupRuleResource)(nil)
	_ resource.ResourceWithValidateConfig = (*securityGroupRuleResource)(nil)
)

var digits = regexp.MustCompile(`^[1-9][0-9]*$`) // an ID: never 0, never zero-padded

func newSecurityGroupRule() resource.Resource { return &securityGroupRuleResource{} }

type securityGroupRuleResource struct{ base }

type securityGroupRuleModel struct {
	ID              types.String `tfsdk:"id"`
	SecurityGroupID types.String `tfsdk:"security_group_id"`
	RuleID          types.String `tfsdk:"rule_id"`
	ruleModel
}

// ruleID is the composite ID "<security group id>/<rule id>". It is built and parsed only here.
func ruleID(groupID, id int64) string { return fmt.Sprintf("%d/%d", groupID, id) }

func parseRuleID(s string) (groupID, id int64, err error) {
	g, r, ok := strings.Cut(s, "/")
	if ok {
		groupID, err = strconv.ParseInt(g, 10, 64)
		if err == nil {
			id, err = strconv.ParseInt(r, 10, 64)
		}
	}
	if !ok || err != nil || groupID <= 0 || id <= 0 {
		return 0, 0, fmt.Errorf("%q is not a security group rule ID: use <security group id>/<rule id>, for example 9501/9701", s)
	}
	return groupID, id, nil
}

func (r *securityGroupRuleResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_security_group_rule"
}

func (r *securityGroupRuleResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	attrs := ruleAttributes(true)
	attrs["id"] = schema.StringAttribute{
		Computed:      true,
		Description:   "`<security group id>/<rule id>`.",
		PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
	}
	attrs["rule_id"] = schema.StringAttribute{
		Computed:      true,
		Description:   "Rule ID.",
		PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
	}
	attrs["security_group_id"] = schema.StringAttribute{
		Required:      true,
		Description:   "The security group (its `id`) the rule belongs to.",
		Validators:    []validator.String{stringvalidator.RegexMatches(digits, "must be a security group id")},
		PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
	}
	resp.Schema = schema.Schema{
		Description: "One rule of a security group, for rules managed apart from the group (for example from another module). " +
			"Leave `rules` unset on that `mtncloud_security_group`: using both on one group makes them undo each other. " +
			"Every change replaces the rule.",
		Attributes: attrs,
	}
}

func (r *securityGroupRuleResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var m securityGroupRuleModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if !resp.Diagnostics.HasError() {
		resp.Diagnostics.Append(validateRule(m.ruleModel, path.Empty())...)
	}
}

func (r *securityGroupRuleResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	if !r.ready(&resp.Diagnostics) {
		return
	}
	var plan securityGroupRuleModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	groupID, _ := strconv.ParseInt(plan.SecurityGroupID.ValueString(), 10, 64)
	want := toRule(plan.ruleModel)
	before, err := r.client.GetSecurityGroup(ctx, groupID)
	if err != nil {
		resp.Diagnostics.AddError("Unable to read security group", err.Error())
		return
	}
	if old := findRule(before.Rules, want, nil); old != nil {
		resp.Diagnostics.AddError("Security group rule already exists",
			fmt.Sprintf("Security group %d already has this rule (ID %d). Import it with `terraform import <address> %s`.",
				groupID, old.ID, ruleID(groupID, old.ID)))
		return
	}
	created, err := r.client.CreateSecurityGroupRule(ctx, groupID, want.toInput())
	if err != nil {
		// The request may have been applied (rule 11): a matching rule that wasn't there before is ours.
		var found *client.SecurityGroupRule
		if client.MayHaveSucceeded(err) && ctx.Err() == nil {
			if after, getErr := r.client.GetSecurityGroup(ctx, groupID); getErr == nil {
				found = findRule(after.Rules, want, before.Rules)
			}
		}
		if found == nil {
			resp.Diagnostics.AddError("Unable to add security group rule", err.Error())
			return
		}
		created = found
	}
	setRule(&plan, groupID, *created)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *securityGroupRuleResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	if !r.ready(&resp.Diagnostics) {
		return
	}
	var state securityGroupRuleModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	groupID, id, err := parseRuleID(state.ID.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Invalid security group rule ID", err.Error())
		return
	}
	g, err := r.client.GetSecurityGroup(ctx, groupID)
	if client.IsNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Unable to read security group", err.Error())
		return
	}
	for _, rl := range g.Rules {
		if rl.ID == id {
			setRule(&state, groupID, rl)
			resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
			return
		}
	}
	resp.State.RemoveResource(ctx) // the rule was deleted outside Terraform
}

// Update is never called: every attribute requires replacement.
func (r *securityGroupRuleResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	resp.Diagnostics.Append(req.Plan.Get(ctx, &securityGroupRuleModel{})...)
	resp.Diagnostics.AddError("Security group rules can't be changed in place", "This is a provider bug: every rule attribute should replace the rule.")
}

func (r *securityGroupRuleResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	if !r.ready(&resp.Diagnostics) {
		return
	}
	var state securityGroupRuleModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	groupID, id, err := parseRuleID(state.ID.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Invalid security group rule ID", err.Error())
		return
	}
	if err := r.client.DeleteSecurityGroupRule(ctx, groupID, id); err != nil {
		resp.Diagnostics.AddError("Unable to remove security group rule", err.Error())
	}
}

func (r *securityGroupRuleResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	groupID, id, err := parseRuleID(req.ID)
	if err != nil {
		resp.Diagnostics.AddError("Invalid import ID", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), ruleID(groupID, id))...)
}

// findRule returns the rule in rules that allows the same traffic as want and is not in skip.
func findRule(rules []client.SecurityGroupRule, want rule, skip []client.SecurityGroupRule) *client.SecurityGroupRule {
	for i, rl := range rules {
		if fromAPI(rl).key() == want.key() && !slices.ContainsFunc(skip, func(s client.SecurityGroupRule) bool { return s.ID == rl.ID }) {
			return &rules[i]
		}
	}
	return nil
}

// setRule copies an API rule into the model. A description left unset stays unset whatever name
// the API returns, so it never shows a diff.
func setRule(m *securityGroupRuleModel, groupID int64, rl client.SecurityGroupRule) {
	keepNoDesc := m.Description.IsNull() && !m.Direction.IsNull()
	f := fromRule(fromAPI(rl))
	if keepNoDesc {
		f.Description = types.StringNull()
	}
	m.ruleModel = f
	m.ID = types.StringValue(ruleID(groupID, rl.ID))
	m.RuleID = types.StringValue(strconv.FormatInt(rl.ID, 10))
	m.SecurityGroupID = types.StringValue(strconv.FormatInt(groupID, 10))
}
