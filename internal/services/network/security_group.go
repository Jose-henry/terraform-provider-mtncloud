package network

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strconv"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/mtn/terraform-provider-mtncloud/internal/client"
	"github.com/mtn/terraform-provider-mtncloud/internal/normalize"
	"github.com/mtn/terraform-provider-mtncloud/internal/validators"
)

var (
	_ resource.ResourceWithConfigure      = (*securityGroupResource)(nil)
	_ resource.ResourceWithImportState    = (*securityGroupResource)(nil)
	_ resource.ResourceWithModifyPlan     = (*securityGroupResource)(nil)
	_ resource.ResourceWithValidateConfig = (*securityGroupResource)(nil)
)

func newSecurityGroup() resource.Resource { return &securityGroupResource{} }

type securityGroupResource struct{ base }

// privateImported marks a state just imported: Read then adopts every rule into "rules".
const privateImported = "imported"

type securityGroupModel struct {
	ID                  types.String `tfsdk:"id"`
	Name                types.String `tfsdk:"name"`
	Description         types.String `tfsdk:"description"`
	Project             types.String `tfsdk:"project"`
	Rules               types.Set    `tfsdk:"rules"`
	RemoveDefaultEgress types.Bool   `tfsdk:"remove_default_egress"`
	AllRules            types.List   `tfsdk:"all_rules"`
	ExternalID          types.String `tfsdk:"external_id"`
}

type ruleModel struct {
	Direction             types.String `tfsdk:"direction"`
	Protocol              types.String `tfsdk:"protocol"`
	PortRange             types.String `tfsdk:"port_range"`
	CIDR                  types.String `tfsdk:"cidr"`
	SourceSecurityGroupID types.String `tfsdk:"source_security_group_id"`
	Description           types.String `tfsdk:"description"`
}

type allRuleModel struct {
	ID                    types.String `tfsdk:"id"`
	Direction             types.String `tfsdk:"direction"`
	Protocol              types.String `tfsdk:"protocol"`
	PortRange             types.String `tfsdk:"port_range"`
	CIDR                  types.String `tfsdk:"cidr"`
	SourceSecurityGroupID types.String `tfsdk:"source_security_group_id"`
	Description           types.String `tfsdk:"description"`
	Default               types.Bool   `tfsdk:"default"`
}

var ruleAttrTypes = map[string]attr.Type{
	"direction": types.StringType, "protocol": types.StringType, "port_range": types.StringType,
	"cidr": types.StringType, "source_security_group_id": types.StringType, "description": types.StringType,
}

var allRuleAttrTypes = map[string]attr.Type{
	"id": types.StringType, "direction": types.StringType, "protocol": types.StringType,
	"port_range": types.StringType, "cidr": types.StringType, "source_security_group_id": types.StringType,
	"description": types.StringType, "default": types.BoolType,
}

func (r *securityGroupResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_security_group"
}

// ruleAttributes are shared with mtncloud_security_group_rule.
func ruleAttributes(replace bool) map[string]schema.Attribute {
	var mods []planmodifier.String
	if replace {
		mods = []planmodifier.String{stringplanmodifier.RequiresReplace()}
	}
	return map[string]schema.Attribute{
		"direction": schema.StringAttribute{
			Required:      true,
			Description:   "`ingress` (traffic to the instances) or `egress` (traffic from them).",
			Validators:    []validator.String{stringvalidator.OneOf("ingress", "egress")},
			PlanModifiers: mods,
		},
		"protocol": schema.StringAttribute{
			Required:      true,
			Description:   "`tcp`, `udp`, `icmp` or `any`.",
			Validators:    []validator.String{stringvalidator.OneOf("tcp", "udp", "icmp", "any")},
			PlanModifiers: mods,
		},
		"port_range": schema.StringAttribute{
			Optional:      true,
			Description:   "A port (`\"22\"`) or a range (`\"8000-8080\"`), for `tcp` and `udp`. Leave it out to allow every port.",
			Validators:    []validator.String{validators.PortRange()},
			PlanModifiers: mods,
		},
		"cidr": schema.StringAttribute{
			Optional: true,
			Description: "The remote addresses, written as a network (`\"10.0.0.0/24\"`, `\"0.0.0.0/0\"`): where traffic " +
				"comes from for `ingress`, where it goes for `egress`. Leave it and `source_security_group_id` out to allow any address.",
			Validators: []validator.String{
				validators.NetworkCIDR(),
				stringvalidator.ConflictsWith(path.MatchRelative().AtParent().AtName("source_security_group_id")),
			},
			PlanModifiers: mods,
		},
		"source_security_group_id": schema.StringAttribute{
			Optional:      true,
			Description:   "Allow the instances in this security group (its `id`) instead of a CIDR.",
			Validators:    []validator.String{stringvalidator.RegexMatches(digits, "must be a security group id")},
			PlanModifiers: mods,
		},
		"description": schema.StringAttribute{
			Optional:      true,
			Description:   "A short note shown in the console. Changing it recreates the rule: it is removed, then added again, so its traffic is briefly blocked.",
			Validators:    []validator.String{stringvalidator.LengthAtLeast(1)},
			PlanModifiers: mods,
		},
	}
}

func (r *securityGroupResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "A security group (firewall) for instances in one project. MTN Cloud adds two rules to every new " +
			"group that allow all outbound traffic (IPv4 and IPv6); they appear in MTN Cloud after its next sync. " +
			"By default Terraform leaves them in place; set `remove_default_egress` to remove them.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:      true,
				Description:   "Security group ID.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"name": schema.StringAttribute{
				Required:    true,
				Description: "Name of the security group.",
				Validators:  []validator.String{stringvalidator.LengthBetween(1, 255)},
			},
			"description": schema.StringAttribute{
				Optional:    true,
				Description: "Description of the security group.",
			},
			// Optional+Computed: left out, the provider's project is used, and state keeps it.
			"project": schema.StringAttribute{
				Optional: true, Computed: true,
				Description: "Project the group is created in, by name (for example `web-prod`; the full name with the tenant suffix also works). " +
					"Defaults to the provider's `project`; changing that default later replaces a group that relies on it. Changing it to another project replaces the group.",
			},
			"rules": schema.SetNestedAttribute{
				Optional: true,
				Description: "The rules Terraform manages. When set, rules added outside Terraform are removed on the next apply, " +
					"except the default egress rules (see `remove_default_egress`). Leave it out to let Terraform ignore the rules, " +
					"for example when they are managed with `mtncloud_security_group_rule`; never use both on one group.",
				NestedObject: schema.NestedAttributeObject{Attributes: ruleAttributes(false)},
			},
			"remove_default_egress": schema.BoolAttribute{
				// Optional+Computed only because a schema default requires Computed.
				Optional: true,
				Computed: true,
				Default:  booldefault.StaticBool(false),
				Description: "Remove the two default egress rules (allow all outbound traffic). Defaults to `false`. " +
					"They are added when the group is created, but MTN Cloud only shows them after its sync, some time later; " +
					"they are removed by the first `terraform apply` after that (`terraform plan` shows the pending change). " +
					"**Until then the group allows all outbound traffic.**",
			},
			"all_rules": schema.ListNestedAttribute{
				Computed: true,
				Description: "Every rule on the group, including the default egress rules and rules managed elsewhere, sorted. " +
					"Read-only: use it to see the group's complete rule set.",
				NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
					"id":                       schema.StringAttribute{Computed: true, Description: "Rule ID."},
					"direction":                schema.StringAttribute{Computed: true, Description: "`ingress` or `egress`."},
					"protocol":                 schema.StringAttribute{Computed: true, Description: "`tcp`, `udp`, `icmp` or `any`."},
					"port_range":               schema.StringAttribute{Computed: true, Description: "Port or range; null for all ports."},
					"cidr":                     schema.StringAttribute{Computed: true, Description: "Remote addresses; null for any address or a group rule."},
					"source_security_group_id": schema.StringAttribute{Computed: true, Description: "Remote security group, for group rules."},
					"description":              schema.StringAttribute{Computed: true, Description: "Rule name in the console."},
					"default":                  schema.BoolAttribute{Computed: true, Description: "Whether this is one of the default egress rules MTN Cloud adds."},
				}},
			},
			"external_id": schema.StringAttribute{
				Computed:      true,
				Description:   "Security group ID in the cloud (the external ID).",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
		},
	}
}

func (r *securityGroupResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var m securityGroupModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() || m.Rules.IsNull() || m.Rules.IsUnknown() {
		return
	}
	var rules []ruleModel
	resp.Diagnostics.Append(m.Rules.ElementsAs(ctx, &rules, false)...)
	seen := map[string]bool{}
	for _, rm := range rules {
		resp.Diagnostics.Append(validateRule(rm, path.Root("rules"))...)
		if !known(rm) {
			continue
		}
		// OpenStack refuses two rules that allow the same traffic; a description doesn't make them different.
		if k := toRule(rm).key(); seen[k] {
			resp.Diagnostics.AddAttributeError(path.Root("rules"), "Duplicate rule",
				fmt.Sprintf("Two rules allow the same %s %s traffic (port %q, remote %q), differing at most in description: keep one.",
					rm.Direction.ValueString(), rm.Protocol.ValueString(), rm.PortRange.ValueString(),
					cmp.Or(rm.CIDR.ValueString(), rm.SourceSecurityGroupID.ValueString(), "any address")))
		} else {
			seen[k] = true
		}
	}
}

// known reports whether every field that identifies a rule is known at plan time.
func known(rm ruleModel) bool {
	return !rm.Protocol.IsUnknown() && !rm.PortRange.IsUnknown() && !rm.Direction.IsUnknown() &&
		!rm.CIDR.IsUnknown() && !rm.SourceSecurityGroupID.IsUnknown()
}

// validateRule holds the checks that span several rule fields.
func validateRule(rm ruleModel, at path.Path) diag.Diagnostics {
	var diags diag.Diagnostics
	if !known(rm) {
		return diags
	}
	p := rm.Protocol.ValueString()
	if (p == "icmp" || p == "any") && !rm.PortRange.IsNull() {
		diags.AddAttributeError(at, "Port range not allowed",
			fmt.Sprintf("A %s rule applies to every port: remove port_range %q, or use tcp or udp.", p, rm.PortRange.ValueString()))
	}
	if toRule(rm).isDefaultEgressShape() {
		diags.AddAttributeError(at, "This is a default egress rule",
			"MTN Cloud already adds an allow-all egress rule for 0.0.0.0/0 and ::/0 to every security group, and refuses a duplicate. "+
				"Remove this rule; the default stays unless remove_default_egress is true.")
	}
	return diags
}

func toRule(rm ruleModel) rule {
	return rule{
		Direction: rm.Direction.ValueString(), Protocol: rm.Protocol.ValueString(),
		PortRange: normalize.PortRange(rm.PortRange.ValueString()), CIDR: rm.CIDR.ValueString(),
		SourceSecurityGroupID: rm.SourceSecurityGroupID.ValueString(), Description: rm.Description.ValueString(),
	}
}

func optional(s string) types.String {
	if s == "" {
		return types.StringNull()
	}
	return types.StringValue(s)
}

func fromRule(r rule) ruleModel {
	return ruleModel{
		Direction: types.StringValue(r.Direction), Protocol: types.StringValue(r.Protocol),
		PortRange: optional(r.PortRange), CIDR: optional(r.CIDR),
		SourceSecurityGroupID: optional(r.SourceSecurityGroupID), Description: optional(r.Description),
	}
}

// desiredRules reads the configured rules. ok is false when "rules" is unset (unmanaged).
func desiredRules(ctx context.Context, set types.Set) (rules []rule, ok bool, diags diag.Diagnostics) {
	if set.IsNull() || set.IsUnknown() {
		return nil, false, diags
	}
	var models []ruleModel
	diags.Append(set.ElementsAs(ctx, &models, false)...)
	for _, m := range models {
		rules = append(rules, toRule(m))
	}
	return rules, true, diags
}

func (r *securityGroupResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.Plan.Raw.IsNull() || r.client == nil {
		return // destroy, or validation before the provider is configured
	}
	if r.fillDefaults(ctx, &req, resp, "project"); resp.Diagnostics.HasError() {
		return
	}
	r.planProject(ctx, req, resp)
	if req.State.Raw.IsNull() || resp.Diagnostics.HasError() {
		return
	}
	var state, plan securityGroupModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// Default egress rules that synced in since the last apply are removed by this one.
	if plan.RemoveDefaultEgress.ValueBool() && hasDefault(ctx, state.AllRules, &resp.Diagnostics) {
		resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("all_rules"), types.ListUnknown(types.ObjectType{AttrTypes: allRuleAttrTypes}))...)
	}
}

func hasDefault(ctx context.Context, list types.List, diags *diag.Diagnostics) bool {
	if list.IsNull() || list.IsUnknown() {
		return false
	}
	var all []allRuleModel
	diags.Append(list.ElementsAs(ctx, &all, false)...)
	return slices.ContainsFunc(all, func(a allRuleModel) bool { return a.Default.ValueBool() })
}

func (r *securityGroupResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	if !r.ready(&resp.Diagnostics) {
		return
	}
	var plan securityGroupModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	desired, managed, diags := desiredRules(ctx, plan.Rules)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	project, err := r.client.ProjectByName(ctx, plan.Project.ValueString())
	if err != nil {
		resp.Diagnostics.AddAttributeError(path.Root("project"), "Unknown project", err.Error())
		return
	}

	name, desc := plan.Name.ValueString(), plan.Description.ValueString()
	// A group of this name already in the project is never adopted: it may not be ours.
	existing, err := r.client.FindSecurityGroup(ctx, name, project.ID)
	if err != nil {
		resp.Diagnostics.AddError("Unable to check for an existing security group", err.Error())
		return
	}
	if existing != nil {
		resp.Diagnostics.AddAttributeError(path.Root("name"), "Security group already exists",
			fmt.Sprintf("Project %q already has a security group named %q (ID %d). Import it with "+
				"`terraform import <address> %d`, or choose another name.", plan.Project.ValueString(), name, existing.ID, existing.ID))
		return
	}
	g, err := r.client.CreateSecurityGroup(ctx, name, desc, project.ID)
	if err != nil {
		// The request may have been applied (rule 11). None existed a moment ago, so one now is ours.
		if !client.MayHaveSucceeded(err) || ctx.Err() != nil {
			resp.Diagnostics.AddError("Unable to create security group", err.Error())
			return
		}
		found, findErr := r.client.FindSecurityGroup(ctx, name, project.ID)
		if findErr != nil || found == nil {
			resp.Diagnostics.AddError("Unable to create security group", err.Error())
			return
		}
		g = found
	}

	// Save the group before adding rules, so a failed rule never leaves it outside Terraform.
	plan.ID = types.StringValue(strconv.FormatInt(g.ID, 10))
	if managed {
		for _, rl := range desired {
			created, err := r.client.CreateSecurityGroupRule(ctx, g.ID, rl.toInput())
			if err != nil {
				resp.Diagnostics.AddError("Unable to add security group rule", fmt.Sprintf("%s rule %s: %s", rl.Direction, rl.key(), err))
				break
			}
			g.Rules = append(g.Rules, *created)
		}
	}
	tenant := r.tenant(ctx, &resp.Diagnostics)
	resp.Diagnostics.Append(refresh(ctx, &plan, g, tenant, managed, desired)...)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *securityGroupResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	if !r.ready(&resp.Diagnostics) {
		return
	}
	var state securityGroupModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	id, err := strconv.ParseInt(state.ID.ValueString(), 10, 64)
	if err != nil {
		resp.Diagnostics.AddError("Invalid security group ID", fmt.Sprintf("%q is not a security group ID: %s", state.ID.ValueString(), err))
		return
	}
	g, err := r.client.GetSecurityGroup(ctx, id)
	if client.IsNotFound(err) {
		resp.State.RemoveResource(ctx) // deleted outside Terraform: plan a new one
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Unable to read security group", err.Error())
		return
	}

	imported, diags := req.Private.GetKey(ctx, privateImported)
	resp.Diagnostics.Append(diags...)
	prior, managed, diags := desiredRules(ctx, state.Rules)
	resp.Diagnostics.Append(diags...)
	if len(imported) > 0 {
		managed = true
		resp.Diagnostics.Append(resp.Private.SetKey(ctx, privateImported, nil)...)
	}
	if state.RemoveDefaultEgress.IsNull() {
		state.RemoveDefaultEgress = types.BoolValue(false)
	}
	tenant := r.tenant(ctx, &resp.Diagnostics)
	resp.Diagnostics.Append(refresh(ctx, &state, g, tenant, managed, prior)...)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *securityGroupResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	if !r.ready(&resp.Diagnostics) {
		return
	}
	var plan, state securityGroupModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	desired, managed, diags := desiredRules(ctx, plan.Rules)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	id, _ := strconv.ParseInt(state.ID.ValueString(), 10, 64)

	if !plan.Name.Equal(state.Name) || !plan.Description.Equal(state.Description) {
		if err := r.client.UpdateSecurityGroup(ctx, id, plan.Name.ValueString(), plan.Description.ValueString()); err != nil {
			resp.Diagnostics.AddError("Unable to update security group", err.Error())
			return
		}
	}
	g, err := r.client.GetSecurityGroup(ctx, id)
	if err != nil {
		resp.Diagnostics.AddError("Unable to read security group", err.Error())
		return
	}
	var deleteIDs []int64
	var create []rule
	if managed {
		deleteIDs, create = planRules(g.Rules, desired)
	}
	if plan.RemoveDefaultEgress.ValueBool() {
		for _, rl := range g.Rules {
			if isDefaultEgress(rl) {
				deleteIDs = append(deleteIDs, rl.ID)
			}
		}
	}
	// On a failure, stop, but still save what the API now has: state never lists a deleted rule.
	failed := false
	for _, ruleID := range deleteIDs {
		if err := r.client.DeleteSecurityGroupRule(ctx, id, ruleID); err != nil {
			resp.Diagnostics.AddError("Unable to remove security group rule", fmt.Sprintf("rule %d: %s", ruleID, err))
			failed = true
			break
		}
	}
	for _, rl := range create {
		if failed {
			break
		}
		if _, err := r.client.CreateSecurityGroupRule(ctx, id, rl.toInput()); err != nil {
			resp.Diagnostics.AddError("Unable to add security group rule", fmt.Sprintf("%s rule %s: %s", rl.Direction, rl.key(), err))
			break
		}
	}
	if g, err = r.client.GetSecurityGroup(ctx, id); err != nil {
		resp.Diagnostics.AddError("Unable to read security group", err.Error())
		return
	}
	plan.ID, plan.ExternalID = state.ID, state.ExternalID
	tenant := r.tenant(ctx, &resp.Diagnostics)
	resp.Diagnostics.Append(refresh(ctx, &plan, g, tenant, managed, desired)...)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *securityGroupResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	if !r.ready(&resp.Diagnostics) {
		return
	}
	var state securityGroupModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	id, _ := strconv.ParseInt(state.ID.ValueString(), 10, 64)
	if err := r.client.DeleteSecurityGroup(ctx, id); err != nil {
		msg := err.Error()
		if client.IsConflict(err) || client.IsBadRequest(err) || client.IsInUse(err) || client.IsRefused(err) {
			msg += ". A group still used by instances can't be deleted: remove it from them first."
		}
		resp.Diagnostics.AddError("Unable to delete security group", msg)
	}
}

func (r *securityGroupResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	if _, err := strconv.ParseInt(req.ID, 10, 64); err != nil {
		resp.Diagnostics.AddError("Invalid import ID", fmt.Sprintf("Import a security group by its numeric ID, for example 9501. Got %q.", req.ID))
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
	resp.Diagnostics.Append(resp.Private.SetKey(ctx, privateImported, []byte("true"))...)
}

// refresh copies the API's view of the group into the model. managed says whether "rules" is
// compared with the API; prior is the rule set the model had (for descriptions left unset).
func refresh(ctx context.Context, m *securityGroupModel, g *client.SecurityGroup, tenant string, managed bool, prior []rule) diag.Diagnostics {
	var diags diag.Diagnostics
	m.ID = types.StringValue(strconv.FormatInt(g.ID, 10))
	m.Name = types.StringValue(g.Name)
	if g.Description != "" || !m.Description.IsNull() {
		m.Description = types.StringValue(g.Description)
	}
	m.ExternalID = types.StringValue(g.ExternalID)
	if len(g.Locations) > 0 {
		loc := g.Locations[0]
		if m.ExternalID.ValueString() == "" {
			m.ExternalID = types.StringValue(loc.ExternalID)
		}
		if m.Project.IsNull() || m.Project.IsUnknown() || !normalize.ProjectMatches(m.Project.ValueString(), loc.ProjectName, tenant) {
			m.Project = types.StringValue(normalize.ProjectShortName(loc.ProjectName, tenant))
		}
	}

	if managed {
		models := []ruleModel{}
		for _, rl := range readRules(g.Rules, prior) {
			models = append(models, fromRule(rl))
		}
		set, d := types.SetValueFrom(ctx, types.ObjectType{AttrTypes: ruleAttrTypes}, models)
		diags.Append(d...)
		m.Rules = set
	} else {
		m.Rules = types.SetNull(types.ObjectType{AttrTypes: ruleAttrTypes})
	}

	list, d := allRules(ctx, g.Rules)
	diags.Append(d...)
	m.AllRules = list
	return diags
}

// allRules is every rule of a group as "all_rules" shows it, sorted by what it allows.
func allRules(ctx context.Context, rules []client.SecurityGroupRule) (types.List, diag.Diagnostics) {
	all := make([]allRuleModel, 0, len(rules))
	sorted := slices.Clone(rules)
	slices.SortFunc(sorted, func(a, b client.SecurityGroupRule) int {
		return cmp.Or(cmp.Compare(fromAPI(a).key(), fromAPI(b).key()), cmp.Compare(a.ID, b.ID))
	})
	for _, rl := range sorted {
		f := fromAPI(rl)
		all = append(all, allRuleModel{
			ID: types.StringValue(strconv.FormatInt(rl.ID, 10)), Direction: types.StringValue(f.Direction),
			Protocol: types.StringValue(f.Protocol), PortRange: optional(f.PortRange), CIDR: optional(f.CIDR),
			SourceSecurityGroupID: optional(f.SourceSecurityGroupID), Description: optional(f.Description),
			Default: types.BoolValue(isDefaultEgress(rl)),
		})
	}
	return types.ListValueFrom(ctx, types.ObjectType{AttrTypes: allRuleAttrTypes}, all)
}
