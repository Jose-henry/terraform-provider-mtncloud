package network

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-framework/types/basetypes"

	"github.com/mtn/terraform-provider-mtncloud/internal/client"
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
	}
}

// objectAsOptions reads a nested object into a struct; unknown values were handled by the caller.
var objectAsOptions = basetypes.ObjectAsOptions{}
