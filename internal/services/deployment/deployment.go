// Package deployment holds the console's Provisioning > Deployments: a deployment, its versions,
// the files of a version, and a version deployed to an instance (B-014, owner 2026-11-01).
package deployment

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/mtn/terraform-provider-mtncloud/internal/client"
)

// Resources lists every resource in this package, for the provider to register.
func Resources() []func() resource.Resource {
	return []func() resource.Resource{newDeployment, newVersion, newFile, newInstanceDeploy}
}

// base gives every resource its client.
type base struct{ client *client.Client }

func (b *base) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	c, ok := req.ProviderData.(*client.Client)
	if !ok {
		resp.Diagnostics.AddError("Unexpected provider data", fmt.Sprintf("expected *client.Client, got %T. This is a provider bug.", req.ProviderData))
		return
	}
	b.client = c
}

func (b *base) ready(diags *diag.Diagnostics) bool {
	if b.client == nil {
		diags.AddError("MTN Cloud provider is not configured yet",
			"The provider block uses values that are only known after apply. Use values known at plan time, or apply the resources they come from first.")
		return false
	}
	return true
}

var (
	digits    = regexp.MustCompile(`^[0-9]+$`)
	idValid   = []validator.String{stringvalidator.RegexMatches(digits, "must be an ID")}
	keep      = []planmodifier.String{stringplanmodifier.UseStateForUnknown()}
	replace   = []planmodifier.String{stringplanmodifier.RequiresReplace()}
	notBlank  = []validator.String{stringvalidator.LengthAtLeast(1)}
	parseID   = func(s string) int64 { n, _ := strconv.ParseInt(s, 10, 64); return n }
	idString  = func(n int64) types.String { return types.StringValue(strconv.FormatInt(n, 10)) }
	nullEmpty = func(s string) types.String {
		if s == "" {
			return types.StringNull()
		}
		return types.StringValue(s)
	}
)

// splitID parses an import ID of n numeric parts joined by "/".
func splitID(id string, n int, form string, diags *diag.Diagnostics) []string {
	parts := strings.Split(id, "/")
	ok := len(parts) == n
	for _, p := range parts {
		ok = ok && digits.MatchString(p)
	}
	if !ok {
		diags.AddError("Invalid import ID", fmt.Sprintf("Expected %s, got %q.", form, id))
		return nil
	}
	return parts
}

// deploymentResource is a deployment record.
type deploymentResource struct{ base }

func newDeployment() resource.Resource { return &deploymentResource{} }

var _ resource.ResourceWithImportState = (*deploymentResource)(nil)

type deploymentModel struct {
	ID          types.String `tfsdk:"id"`
	Name        types.String `tfsdk:"name"`
	Description types.String `tfsdk:"description"`
}

func (r *deploymentResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_deployment"
}

func (r *deploymentResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "A deployment (Provisioning > Deployments): a named application whose versions " +
			"(`mtncloud_deployment_version`) are deployed to instances with `mtncloud_instance_deploy`. Destroying it deletes " +
			"its versions too; what was deployed stays on the instances.",
		Attributes: map[string]schema.Attribute{
			"id":   schema.StringAttribute{Computed: true, PlanModifiers: keep, Description: "ID of the deployment."},
			"name": schema.StringAttribute{Required: true, Validators: notBlank, Description: "Name, unique in the tenant. Changed in place."},
			"description": schema.StringAttribute{Optional: true, Validators: notBlank,
				Description: "Description. Leave it out for none. Changed in place."},
		},
	}
}

func (m *deploymentModel) fill(d *client.Deployment) {
	m.ID, m.Name, m.Description = idString(d.ID), types.StringValue(d.Name), nullEmpty(d.Description)
}

func (r *deploymentResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	if !r.ready(&resp.Diagnostics) {
		return
	}
	var m deploymentModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	// A deployment with this name may be another config's, or this one's from a create that timed
	// out: never taken over, it is named so it can be imported.
	existing, err := r.client.DeploymentsNamed(ctx, m.Name.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Unable to look for deployment "+m.Name.ValueString(), err.Error())
		return
	}
	if len(existing) > 0 {
		resp.Diagnostics.AddAttributeError(path.Root("name"), "Deployment already exists",
			fmt.Sprintf("A deployment named %q already exists (ID %d). Import it to manage it here (terraform import <address> %d), "+
				"or give this one another name.", existing[0].Name, existing[0].ID, existing[0].ID))
		return
	}
	d, err := r.client.CreateDeployment(ctx, m.Name.ValueString(), m.Description.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Unable to create deployment "+m.Name.ValueString(), err.Error())
		return
	}
	m.fill(d)
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

func (r *deploymentResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	if !r.ready(&resp.Diagnostics) {
		return
	}
	var m deploymentModel
	resp.Diagnostics.Append(req.State.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	d, err := r.client.GetDeployment(ctx, parseID(m.ID.ValueString()))
	if client.IsNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Unable to read deployment "+m.ID.ValueString(), err.Error())
		return
	}
	m.fill(d)
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

func (r *deploymentResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	if !r.ready(&resp.Diagnostics) {
		return
	}
	var m, state deploymentModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &m)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	// An empty description clears it.
	d, err := r.client.UpdateDeployment(ctx, parseID(state.ID.ValueString()), m.Name.ValueString(), m.Description.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Unable to update deployment "+state.ID.ValueString(), err.Error())
		return
	}
	m.fill(d)
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

func (r *deploymentResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	if !r.ready(&resp.Diagnostics) {
		return
	}
	var m deploymentModel
	resp.Diagnostics.Append(req.State.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.client.DeleteDeployment(ctx, parseID(m.ID.ValueString())); err != nil {
		resp.Diagnostics.AddError("Unable to delete deployment "+m.ID.ValueString(), err.Error())
	}
}

func (r *deploymentResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	if splitID(req.ID, 1, "the deployment's ID", &resp.Diagnostics) == nil {
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
}
