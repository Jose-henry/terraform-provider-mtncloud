package network

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-framework/types/basetypes"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"github.com/mtn/terraform-provider-mtncloud/internal/client"
	"github.com/mtn/terraform-provider-mtncloud/internal/planmodifiers"
)

// Resources lists every resource in this package, for the provider to register.
func Resources() []func() resource.Resource {
	return []func() resource.Resource{
		newSecurityGroup,
		newSecurityGroupRule,
		newNetwork,
		newRouter,
	}
}

// base gives every resource its client.
type base struct {
	client *client.Client
}

func (b *base) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return // validate or an unconfigured provider; CRUD checks ready()
	}
	c, ok := req.ProviderData.(*client.Client)
	if !ok {
		resp.Diagnostics.AddError("Unexpected provider data", fmt.Sprintf("expected *client.Client, got %T. This is a provider bug.", req.ProviderData))
		return
	}
	b.client = c
}

// ready reports whether the provider is configured; see lookup.base.ready for when it isn't.
func (b *base) ready(diags *diag.Diagnostics) bool {
	if b.client == nil {
		diags.AddError("MTN Cloud provider is not configured yet",
			"The provider block uses values that are only known after apply. Use values known at plan time, or apply the resources they come from first.")
		return false
	}
	return true
}

// tenant is the caller's tenant name, which ends every full project name.
func (b *base) tenant(ctx context.Context, diags *diag.Diagnostics) string {
	who, err := b.client.WhoAmI(ctx)
	if err != nil {
		diags.AddError("Unable to read your tenant", err.Error())
		return ""
	}
	return who.AccountName
}

// planProject handles the "project" attribute of a resource that lives in one project. On create
// it checks the name at plan time (rule 17). On update, "web-prod" and "web-prod-AcmeCorp" are the
// same project: only a move to another project replaces the resource.
func (b *base) planProject(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	at := path.Root("project")
	var plan, state types.String
	resp.Diagnostics.Append(req.Plan.GetAttribute(ctx, at, &plan)...)
	if resp.Diagnostics.HasError() || plan.IsNull() {
		return
	}
	if req.State.Raw.IsNull() {
		if !plan.IsUnknown() {
			if _, err := b.client.ProjectByName(ctx, plan.ValueString()); err != nil {
				resp.Diagnostics.AddAttributeError(at, "Unknown project", err.Error())
			}
		}
		return
	}
	resp.Diagnostics.Append(req.State.GetAttribute(ctx, at, &state)...)
	// Null after an import that couldn't tell the project (a router without networks): adopt the
	// configured one rather than replace a resource that was only just imported.
	if resp.Diagnostics.HasError() || plan.Equal(state) || state.IsNull() {
		return
	}
	if plan.IsUnknown() {
		resp.RequiresReplace.Append(at)
		return
	}
	from, errFrom := b.client.ProjectByName(ctx, state.ValueString())
	to, errTo := b.client.ProjectByName(ctx, plan.ValueString())
	if errTo != nil {
		resp.Diagnostics.AddAttributeError(at, "Unknown project", errTo.Error())
		return
	}
	if errFrom != nil || from.ID != to.ID {
		resp.RequiresReplace.Append(at)
		return
	}
	// The same project spelled another way ("web-prod" vs "web-prod-Acme", or a provider default):
	// no change, the state's spelling stays.
	resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, at, state)...)
	keepStateIfUnchanged(req, resp)
}

// keepStateIfUnchanged undoes the framework marking computed values unknown when the configuration
// differed from state only in a spelling ModifyPlan has put back: if the plan, with each unknown
// read from state, is the state, the plan is the state. Only with a fully known configuration: an
// unknown there is a real pending change.
func keepStateIfUnchanged(req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if !req.Config.Raw.IsFullyKnown() || resp.Diagnostics.HasError() {
		return
	}
	filled, err := tftypes.Transform(resp.Plan.Raw, func(p *tftypes.AttributePath, v tftypes.Value) (tftypes.Value, error) {
		if v.IsKnown() {
			return v, nil
		}
		was, _, err := tftypes.WalkAttributePath(req.State.Raw, p)
		if err != nil {
			return v, nil //nolint:nilerr // no such path in state: leave the value unknown
		}
		if s, ok := was.(tftypes.Value); ok {
			return s, nil
		}
		return v, nil
	})
	if err == nil && filled.Equal(req.State.Raw) {
		resp.Plan.Raw = req.State.Raw
	}
}

// objectAsOptions reads a nested object into a struct; unknown values were handled by the caller.
var objectAsOptions = basetypes.ObjectAsOptions{}

// fillDefaults fills the attributes the configuration leaves out from the provider-wide defaults
// (the provider block, or env), in resp.Plan and in req.Plan, which the rest of ModifyPlan reads.
func (b *base) fillDefaults(ctx context.Context, req *resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse, attrs ...string) {
	d := b.client.Defaults()
	values := map[string][2]string{
		"project": {d.Project, "MTNCLOUD_PROJECT"}, "group": {d.Group, "MTNCLOUD_GROUP"}, "cloud": {d.Cloud, "MTNCLOUD_CLOUD"},
	}
	for _, a := range attrs {
		resp.Diagnostics.Append(planmodifiers.FillDefault(ctx, req.Config, &resp.Plan, a, values[a][1], values[a][0])...)
	}
	req.Plan = resp.Plan
}
