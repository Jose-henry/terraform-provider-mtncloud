package deployment

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-framework-timeouts/resource/timeouts"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/mtn/terraform-provider-mtncloud/internal/client"
)

// instanceDeployResource keeps one deployment's version deployed on an instance. A new version
// (or new config or triggers) deploys again in place; the deploy it replaces becomes archived.
type instanceDeployResource struct{ base }

func newInstanceDeploy() resource.Resource { return &instanceDeployResource{} }

var (
	_ resource.ResourceWithImportState = (*instanceDeployResource)(nil)
	_ resource.ResourceWithModifyPlan  = (*instanceDeployResource)(nil)
)

type instanceDeployModel struct {
	ID           types.String   `tfsdk:"id"`
	InstanceID   types.String   `tfsdk:"instance_id"`
	DeploymentID types.String   `tfsdk:"deployment_id"`
	VersionID    types.String   `tfsdk:"version_id"`
	Config       types.Map      `tfsdk:"config"`
	Triggers     types.Map      `tfsdk:"triggers"`
	DeployID     types.String   `tfsdk:"deploy_id"`
	RunID        types.String   `tfsdk:"run_id"`
	Version      types.String   `tfsdk:"version"`
	Status       types.String   `tfsdk:"status"`
	Timeouts     timeouts.Value `tfsdk:"timeouts"`
}

const deployTimeout = 30 * time.Minute

// runAppears is how long a deploy's run may take to show in the instance's history (live: 5 s).
const runAppears = 2 * time.Minute

func (r *instanceDeployResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_instance_deploy"
}

func (r *instanceDeployResource) Schema(ctx context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Deploys a version of a deployment to an instance, as the instance's Deploy tab does, and keeps it " +
			"deployed: a new `version_id`, `config` or `triggers` deploys again in place (the deploy it replaces is kept as " +
			"archived, for a rollback in the console). The apply waits for the deploy's run on the instance and fails with " +
			"its message if it fails. When someone deploys another version of the same deployment, or a deploy's run " +
			"fails, the plan deploys this one again. Deploys to one instance run one at a time. The instance's image must " +
			"have a deploy folder: MTN's standard images don't yet, and deploying to them fails with \"missing a deploy " +
			"folder configuration\". MTN Cloud can't undeploy, so destroying this leaves the files on the instance.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{Computed: true, PlanModifiers: keep, Description: "`<instance_id>/<deployment_id>`."},
			"instance_id": schema.StringAttribute{Required: true, Validators: idValid, PlanModifiers: replace,
				Description: "The instance, for example `mtncloud_instance.web.id`."},
			"deployment_id": schema.StringAttribute{Required: true, Validators: idValid, PlanModifiers: replace,
				Description: "The deployment, for example `mtncloud_deployment.web.id`."},
			"version_id": schema.StringAttribute{Required: true, Validators: idValid,
				Description: "The version to deploy, for example `mtncloud_deployment_version.v2.id`. Changing it deploys that version in place."},
			"config": schema.MapAttribute{Optional: true, ElementType: types.StringType,
				Description: "Settings for the deploy that depend on the instance's type. Changing them deploys again."},
			"triggers": schema.MapAttribute{Optional: true, ElementType: types.StringType,
				Description: "Any values: changing one deploys the same version again, for example after its files change."},
			"deploy_id": schema.StringAttribute{Computed: true, Description: "ID of the current deploy."},
			"run_id": schema.StringAttribute{Computed: true,
				Description: "ID of the deploy's run in the instance's history (its History tab). Empty when no run was found."},
			"version": schema.StringAttribute{Computed: true, Description: "The deployed version's name."},
			"status": schema.StringAttribute{Computed: true,
				Description: "The deploy's status: `committed` when it is live, `failed` when it or its run failed."},
		},
		Blocks: map[string]schema.Block{
			"timeouts": timeouts.Block(ctx, timeouts.Opts{Create: true, Update: true}),
		},
	}
}

// ModifyPlan shows the deploy's values as known after apply when it deploys again, and keeps them
// otherwise.
func (r *instanceDeployResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.Plan.Raw.IsNull() || req.State.Raw.IsNull() {
		return
	}
	var plan, state instanceDeployModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if sameDeploy(plan, state) {
		plan.DeployID, plan.RunID, plan.Version, plan.Status = state.DeployID, state.RunID, state.Version, state.Status
	} else {
		plan.DeployID, plan.RunID = types.StringUnknown(), types.StringUnknown()
		plan.Version, plan.Status = types.StringUnknown(), types.StringUnknown()
	}
	resp.Diagnostics.Append(resp.Plan.Set(ctx, &plan)...)
}

func sameDeploy(a, b instanceDeployModel) bool {
	x := []attr.Value{a.VersionID, a.Config, a.Triggers}
	y := []attr.Value{b.VersionID, b.Config, b.Triggers}
	for i := range x {
		if !x[i].Equal(y[i]) {
			return false
		}
	}
	return true
}

func (m *instanceDeployModel) fill(d *client.InstanceDeploy, run *client.DeployRun) {
	m.DeployID, m.VersionID, m.Status = idString(d.ID), idString(d.VersionID), types.StringValue(d.Status)
	m.Version = types.StringValue(d.Version)
	m.RunID = types.StringValue("")
	if run != nil {
		m.RunID = idString(run.ID)
	}
}

// deploy deploys the planned version and waits for its run. Warnings say when how it went can't be
// told; an error when it failed.
func (r *instanceDeployResource) deploy(ctx context.Context, m *instanceDeployModel, diags *diag.Diagnostics) error {
	cfg := map[string]any{}
	if !m.Config.IsNull() {
		var c map[string]string
		diags.Append(m.Config.ElementsAs(ctx, &c, false)...)
		if diags.HasError() {
			return fmt.Errorf("config can't be read")
		}
		for k, v := range c {
			cfg[k] = v
		}
	}
	inst := parseID(m.InstanceID.ValueString())
	// One deploy at a time on an instance, until its run is known: the run is then the first after it.
	unlock, err := r.client.LockInstanceDeploys(ctx, inst)
	if err != nil {
		return err
	}
	defer unlock()
	d, err := r.client.DeployToInstance(ctx, inst, parseID(m.VersionID.ValueString()), cfg)
	if err != nil {
		return fmt.Errorf("MTN Cloud refused the deploy: %w", err)
	}
	m.ID = types.StringValue(m.InstanceID.ValueString() + "/" + m.DeploymentID.ValueString())
	got, err := r.client.WaitForDeploy(ctx, inst, d.ID)
	if got != nil {
		got.Created = firstKnown(got.Created, d.Created)
		d = got
	}
	if d.VersionID == 0 {
		d.VersionID = parseID(m.VersionID.ValueString())
	}
	m.fill(d, nil)
	if err != nil {
		return fmt.Errorf("deploy %d didn't finish (last status %q): %w. See the instance's Deploy tab", d.ID, d.Status, err)
	}
	if _, failed := client.DeployDone(d.Status); failed {
		m.Status = types.StringValue("failed")
		return fmt.Errorf("deploy %d ended %q. See the instance's Deploy tab and its history; the next apply deploys again", d.ID, d.Status)
	}
	// The deploy's status says committed even when its run failed: the run decides.
	run, err := r.client.WaitForDeployRun(ctx, d, runAppears)
	if err != nil {
		return fmt.Errorf("deploy %d: unable to follow its run: %w. See the instance's History tab", d.ID, err)
	}
	m.fill(d, run)
	if run == nil {
		diags.AddAttributeWarning(path.Root("status"), "Deploy run not found",
			fmt.Sprintf("Deploy %d is %q, but no run of it showed in instance %d's history within %s, so whether its files "+
				"reached the instance isn't known. See the instance's History tab.", d.ID, d.Status, inst, runAppears))
		return nil
	}
	if _, failed := run.Done(); failed {
		m.Status = types.StringValue("failed")
		why := run.Message
		if why == "" {
			why = "MTN Cloud gave no reason"
		}
		return fmt.Errorf("deploy %d failed on the instance: %s See the instance's History tab; the next apply deploys again", d.ID, sentence(why))
	}
	return nil
}

func firstKnown(a, b time.Time) time.Time {
	if a.IsZero() {
		return b
	}
	return a
}

// sentence ends s with a full stop.
func sentence(s string) string {
	s = strings.TrimSpace(s)
	if strings.HasSuffix(s, ".") {
		return s
	}
	return s + "."
}

func (r *instanceDeployResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	if !r.ready(&resp.Diagnostics) {
		return
	}
	var m instanceDeployModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &m)...)
	timeout, d := m.Timeouts.Create(ctx, deployTimeout)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if err := r.deploy(ctx, &m, &resp.Diagnostics); err != nil {
		// Nothing is kept: the next apply deploys again.
		resp.Diagnostics.AddError("Deploy failed", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

// current is the live deploy of the deployment on the instance (the newest committed one; without
// one, the newest) and when the next deploy on the instance, of any deployment, was made (zero when
// none was). gone when the instance is.
func (r *instanceDeployResource) current(ctx context.Context, m *instanceDeployModel) (d *client.InstanceDeploy, next time.Time, gone bool, err error) {
	ds, err := r.client.InstanceDeploys(ctx, parseID(m.InstanceID.ValueString()))
	if client.IsNotFound(err) {
		return nil, next, true, nil
	}
	if err != nil {
		return nil, next, false, err
	}
	dep := parseID(m.DeploymentID.ValueString())
	var live *client.InstanceDeploy
	for i := range ds {
		if ds[i].DeploymentID != dep {
			continue
		}
		if d == nil || ds[i].ID > d.ID {
			d = &ds[i]
		}
		if strings.EqualFold(ds[i].Status, "committed") && (live == nil || ds[i].ID > live.ID) {
			live = &ds[i]
		}
	}
	if live != nil {
		d = live
	}
	if d == nil {
		return nil, next, false, nil
	}
	for i := range ds {
		if ds[i].ID > d.ID && !ds[i].Created.IsZero() && (next.IsZero() || ds[i].Created.Before(next)) {
			next = ds[i].Created
		}
	}
	return d, next, false, nil
}

func (r *instanceDeployResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	if !r.ready(&resp.Diagnostics) {
		return
	}
	var m instanceDeployModel
	resp.Diagnostics.Append(req.State.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	d, next, gone, err := r.current(ctx, &m)
	if err != nil {
		resp.Diagnostics.AddError("Unable to read the instance's deploys", err.Error())
		return
	}
	if gone || d == nil {
		resp.State.RemoveResource(ctx)
		return
	}
	_, failed := client.DeployDone(d.Status)
	var run *client.DeployRun
	if !failed {
		// The run found at apply is read by its ID; another deploy's run is looked for in the history.
		if m.DeployID.ValueString() == idString(d.ID).ValueString() && m.RunID.ValueString() != "" {
			run, err = r.client.GetDeployRun(ctx, parseID(m.RunID.ValueString()))
			if client.IsNotFound(err) {
				run, err = nil, nil
			}
		}
		if run == nil && err == nil {
			run, err = r.client.DeployRunFor(ctx, d, next)
		}
		if err != nil {
			resp.Diagnostics.AddError("Unable to read the instance's history", err.Error())
			return
		}
		if run != nil {
			_, failed = run.Done()
		}
	}
	// Another version deployed since reads as that version, so the plan deploys this one again. A
	// deploy that failed, or whose run failed, reads as no version: the plan deploys again.
	m.fill(d, run)
	if failed {
		m.VersionID, m.Status = types.StringValue("0"), types.StringValue("failed")
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

func (r *instanceDeployResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var m, state instanceDeployModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &m)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	m.ID = state.ID
	if sameDeploy(m, state) {
		m.DeployID, m.RunID, m.Version, m.Status = state.DeployID, state.RunID, state.Version, state.Status
		resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
		return
	}
	if !r.ready(&resp.Diagnostics) {
		return
	}
	timeout, d := m.Timeouts.Update(ctx, deployTimeout)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if err := r.deploy(ctx, &m, &resp.Diagnostics); err != nil {
		resp.Diagnostics.AddError("Deploy failed", err.Error())
		// The previous values stay, so the plan still shows this change and the next apply deploys again.
		resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

// Delete leaves the deploy: MTN Cloud can't undeploy, and deletes only archived deploys.
func (r *instanceDeployResource) Delete(context.Context, resource.DeleteRequest, *resource.DeleteResponse) {
}

// ImportState takes <instance_id>/<deployment_id>: the live deploy of that deployment.
func (r *instanceDeployResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	p := splitID(req.ID, 2, "<instance_id>/<deployment_id>", &resp.Diagnostics)
	if p == nil {
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("instance_id"), p[0])...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("deployment_id"), p[1])...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("version_id"), "0")...)
}
