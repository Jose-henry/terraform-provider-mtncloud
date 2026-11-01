package deployment

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/mtn/terraform-provider-mtncloud/internal/client"
)

// versionResource is one version of a deployment.
type versionResource struct{ base }

func newVersion() resource.Resource { return &versionResource{} }

var (
	_ resource.ResourceWithImportState    = (*versionResource)(nil)
	_ resource.ResourceWithValidateConfig = (*versionResource)(nil)
)

type versionModel struct {
	ID           types.String `tfsdk:"id"`
	DeploymentID types.String `tfsdk:"deployment_id"`
	Version      types.String `tfsdk:"version"`
	Type         types.String `tfsdk:"type"`
	FetchURL     types.String `tfsdk:"fetch_url"`
	GitURL       types.String `tfsdk:"git_url"`
	GitRef       types.String `tfsdk:"git_ref"`
	Status       types.String `tfsdk:"status"`
}

func (r *versionResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_deployment_version"
}

func (r *versionResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "A version of a deployment. Its content is files uploaded to MTN Cloud (`type = \"file\"`, with " +
			"`mtncloud_deployment_file`), a git repository (`git`), or a URL MTN Cloud fetches when it deploys (`fetch`). " +
			"Everything here changes in place. Destroying it deletes the version and its files; what was deployed stays " +
			"on the instances.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{Computed: true, PlanModifiers: keep, Description: "ID of the version."},
			"deployment_id": schema.StringAttribute{Required: true, Validators: idValid, PlanModifiers: replace,
				Description: "The deployment, for example `mtncloud_deployment.web.id`. Changing it makes the version in the other deployment."},
			"version": schema.StringAttribute{Required: true, Validators: notBlank,
				Description: "The version's name, for example `1.4.0`."},
			"type": schema.StringAttribute{Required: true, Validators: []validator.String{stringvalidator.OneOf("file", "git", "fetch")},
				Description: "Where the content comes from: `file` (uploaded with `mtncloud_deployment_file`), `git` or `fetch`."},
			"fetch_url": schema.StringAttribute{Optional: true, Validators: notBlank,
				Description: "With `type = \"fetch\"`: the URL MTN Cloud downloads when it deploys."},
			"git_url": schema.StringAttribute{Optional: true, Validators: notBlank,
				Description: "With `type = \"git\"`: the repository's URL."},
			"git_ref": schema.StringAttribute{Optional: true, Validators: notBlank,
				Description: "With `type = \"git\"`: the branch, tag or commit. Leave it out for the repository's default."},
			"status": schema.StringAttribute{Computed: true, Description: "The version's status, for example `created`."},
		},
	}
}

func (r *versionResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var m versionModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() || m.Type.IsUnknown() {
		return
	}
	need := map[string]bool{"fetch_url": m.Type.ValueString() == "fetch", "git_url": m.Type.ValueString() == "git"}
	allowed := map[string]bool{"fetch_url": need["fetch_url"], "git_url": need["git_url"], "git_ref": need["git_url"]}
	vals := map[string]types.String{"fetch_url": m.FetchURL, "git_url": m.GitURL, "git_ref": m.GitRef}
	for _, a := range []string{"fetch_url", "git_url", "git_ref"} {
		v := vals[a]
		switch {
		case need[a] && v.IsNull():
			resp.Diagnostics.AddAttributeError(path.Root(a), "Missing "+a, fmt.Sprintf("A %s version needs %s.", m.Type.ValueString(), a))
		case !allowed[a] && !v.IsNull() && !v.IsUnknown():
			resp.Diagnostics.AddAttributeError(path.Root(a), "Unused "+a, fmt.Sprintf("%s is only used with type = %q; this version is %q.",
				a, map[string]string{"fetch_url": "fetch", "git_url": "git", "git_ref": "git"}[a], m.Type.ValueString()))
		}
	}
}

func (m *versionModel) input() client.DeploymentVersionInput {
	return client.DeploymentVersionInput{Version: m.Version.ValueString(), DeployType: m.Type.ValueString(),
		FetchURL: m.FetchURL.ValueString(), GitURL: m.GitURL.ValueString(), GitRef: m.GitRef.ValueString()}
}

func (m *versionModel) fill(v *client.DeploymentVersion) {
	m.ID, m.DeploymentID = idString(v.ID), idString(v.DeploymentID)
	m.Version, m.Type, m.Status = types.StringValue(v.Version), types.StringValue(v.DeployType), types.StringValue(v.Status)
	m.FetchURL, m.GitURL, m.GitRef = nullEmpty(v.FetchURL), nullEmpty(v.GitURL), nullEmpty(v.GitRef)
}

func (r *versionResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	if !r.ready(&resp.Diagnostics) {
		return
	}
	var m versionModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	dep := parseID(m.DeploymentID.ValueString())
	// A version with this name may be another config's, or this one's from a create that timed out:
	// never taken over, it is named so it can be imported.
	vs, err := r.client.DeploymentVersions(ctx, dep)
	if err != nil {
		resp.Diagnostics.AddError("Unable to read deployment "+m.DeploymentID.ValueString()+"'s versions", err.Error())
		return
	}
	for _, v := range vs {
		if v.Version == m.Version.ValueString() {
			resp.Diagnostics.AddAttributeError(path.Root("version"), "Version already exists",
				fmt.Sprintf("Deployment %d already has version %q (ID %d). Import it to manage it here (terraform import <address> %d/%d), "+
					"or give this one another version.", dep, v.Version, v.ID, dep, v.ID))
			return
		}
	}
	v, err := r.client.CreateDeploymentVersion(ctx, dep, m.input())
	if err != nil {
		resp.Diagnostics.AddError("Unable to create version "+m.Version.ValueString(), err.Error())
		return
	}
	if v.DeploymentID == 0 {
		v.DeploymentID = dep
	}
	m.fill(v)
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

func (r *versionResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	if !r.ready(&resp.Diagnostics) {
		return
	}
	var m versionModel
	resp.Diagnostics.Append(req.State.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	dep := parseID(m.DeploymentID.ValueString())
	v, err := r.client.GetDeploymentVersion(ctx, dep, parseID(m.ID.ValueString()))
	if client.IsNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Unable to read version "+m.ID.ValueString(), err.Error())
		return
	}
	if v.DeploymentID == 0 {
		v.DeploymentID = dep
	}
	m.fill(v)
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

func (r *versionResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	if !r.ready(&resp.Diagnostics) {
		return
	}
	var m, state versionModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &m)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	dep := parseID(state.DeploymentID.ValueString())
	v, err := r.client.UpdateDeploymentVersion(ctx, dep, parseID(state.ID.ValueString()), m.input())
	if err != nil {
		resp.Diagnostics.AddError("Unable to update version "+state.ID.ValueString(), err.Error())
		return
	}
	if v.DeploymentID == 0 {
		v.DeploymentID = dep
	}
	m.fill(v)
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

func (r *versionResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	if !r.ready(&resp.Diagnostics) {
		return
	}
	var m versionModel
	resp.Diagnostics.Append(req.State.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.client.DeleteDeploymentVersion(ctx, parseID(m.DeploymentID.ValueString()), parseID(m.ID.ValueString())); err != nil {
		resp.Diagnostics.AddError("Unable to delete version "+m.ID.ValueString(), err.Error())
	}
}

// ImportState takes <deployment_id>/<version_id>.
func (r *versionResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	p := splitID(req.ID, 2, "<deployment_id>/<version_id>", &resp.Diagnostics)
	if p == nil {
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("deployment_id"), p[0])...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), p[1])...)
}
