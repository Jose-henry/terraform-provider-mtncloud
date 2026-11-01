package automation

import (
	"context"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-framework-timeouts/resource/timeouts"
	"github.com/hashicorp/terraform-plugin-framework-validators/setvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/mtn/terraform-provider-mtncloud/internal/client"
)

// instanceWorkflowResource runs an operational workflow on instances, as the console's Actions >
// Run Workflow does (B-014, owner 2026-11-01): on create, and again when the workflow, the
// instances, the inputs or triggers change. A run isn't an object MTN Cloud keeps, so there is
// nothing to read back or delete.
type instanceWorkflowResource struct{ base }

func newInstanceWorkflow() resource.Resource { return &instanceWorkflowResource{} }

var _ resource.ResourceWithModifyPlan = (*instanceWorkflowResource)(nil)

type instanceWorkflowModel struct {
	ID           types.String   `tfsdk:"id"`
	Workflow     types.String   `tfsdk:"workflow"`
	WorkflowID   types.Int64    `tfsdk:"workflow_id"`
	InstanceIDs  types.Set      `tfsdk:"instance_ids"`
	Inputs       types.Map      `tfsdk:"inputs"`
	SecretInputs types.Map      `tfsdk:"secret_inputs"`
	Triggers     types.Map      `tfsdk:"triggers"`
	ExecutionID  types.Int64    `tfsdk:"execution_id"`
	Status       types.String   `tfsdk:"status"`
	Timeouts     timeouts.Value `tfsdk:"timeouts"`
}

var digitsOnly = regexp.MustCompile(`^[0-9]+$`)

const workflowTimeout = 30 * time.Minute

func (r *instanceWorkflowResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_instance_workflow"
}

func (r *instanceWorkflowResource) Schema(ctx context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	keep := []planmodifier.String{stringplanmodifier.UseStateForUnknown()}
	resp.Schema = schema.Schema{
		Description: "Runs an operational workflow on instances, as the console's Actions > Run Workflow does, for example " +
			"MTN's `Create Linux User Git` or `Update Linux User Passwd`. It runs when this is created, and again on every instance " +
			"whenever `workflow`, `instance_ids`, `inputs`, `secret_inputs` or `triggers` change. The apply waits for the run " +
			"and fails with the workflow's own message if it fails; the next apply runs it again. A run isn't kept by MTN " +
			"Cloud as an object, so it can't be imported, and destroying this undoes nothing on the instances (a user it " +
			"made stays, for example).",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{Computed: true, PlanModifiers: keep,
				Description: "The first run's job execution ID."},
			"workflow": schema.StringAttribute{Required: true, Validators: []validator.String{stringvalidator.LengthAtLeast(1)},
				Description: "Name of the workflow, as in the console (Library > Automation > Workflows), for example " +
					"`Create Linux User Git`. It must be an operational workflow."},
			"workflow_id": schema.Int64Attribute{Computed: true, Description: "The workflow's ID."},
			"instance_ids": schema.SetAttribute{Required: true, ElementType: types.StringType,
				Validators: []validator.Set{setvalidator.SizeAtLeast(1), setvalidator.ValueStringsAre(
					stringvalidator.RegexMatches(digitsOnly, "must be an instance ID"))},
				Description: "The instances to run it on, for example `[mtncloud_instance.web.id]`. Each needs the MTN Cloud " +
					"agent (`install_agent`): without it the run fails."},
			"inputs": schema.MapAttribute{Optional: true, ElementType: types.StringType,
				Description: "The workflow's form fields, by field name (the `fieldName` in its inputs), for example " +
					"`{ linuxUser = \"deploy\", makeSudoer = \"on\" }`. Checked against the workflow's form at plan time. " +
					"A password field goes in `secret_inputs`."},
			"secret_inputs": schema.MapAttribute{Optional: true, Sensitive: true, ElementType: types.StringType,
				Description: "Form fields that are secret, such as passwords. Never shown in plans or errors; kept in state (sensitive)."},
			"triggers": schema.MapAttribute{Optional: true, ElementType: types.StringType,
				Description: "Any values: changing one runs the workflow again with the same inputs."},
			"execution_id": schema.Int64Attribute{Computed: true, Description: "The last run's job execution ID."},
			"status":       schema.StringAttribute{Computed: true, Description: "The last run's status."},
		},
		Blocks: map[string]schema.Block{
			"timeouts": timeouts.Block(ctx, timeouts.Opts{Create: true, Update: true}),
		},
	}
}

// runInputs are what changes a run: anything else (timeouts) changes nothing on the instances.
func runInputs(m instanceWorkflowModel) []attr.Value {
	return []attr.Value{m.Workflow, m.InstanceIDs, m.Inputs, m.SecretInputs, m.Triggers}
}

func sameRun(a, b instanceWorkflowModel) bool {
	x, y := runInputs(a), runInputs(b)
	for i := range x {
		if !x[i].Equal(y[i]) {
			return false
		}
	}
	return true
}

// ModifyPlan resolves the workflow and checks the inputs against its form, so a wrong field is
// refused before anything runs. A change that runs the workflow again shows the run's values as
// known after apply.
func (r *instanceWorkflowResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.Plan.Raw.IsNull() {
		return // destroy: nothing to undo
	}
	var plan instanceWorkflowModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var state *instanceWorkflowModel
	if !req.State.Raw.IsNull() {
		state = &instanceWorkflowModel{}
		resp.Diagnostics.Append(req.State.Get(ctx, state)...)
	}
	if state != nil && sameRun(plan, *state) {
		return
	}
	plan.ExecutionID, plan.Status = types.Int64Unknown(), types.StringUnknown()
	if r.client != nil && !plan.Workflow.IsUnknown() {
		wf, msg := r.resolve(ctx, plan.Workflow.ValueString())
		if msg != "" {
			resp.Diagnostics.AddAttributeError(path.Root("workflow"), "Unknown workflow", msg)
			return
		}
		plan.WorkflowID = types.Int64Value(wf.ID)
		checkInputs(ctx, wf, plan, resp)
	} else {
		plan.WorkflowID = types.Int64Unknown()
	}
	resp.Diagnostics.Append(resp.Plan.Set(ctx, &plan)...)
}

// resolve finds the workflow by name; msg says why when it can't be used.
func (r *instanceWorkflowResource) resolve(ctx context.Context, name string) (*client.Workflow, string) {
	ws, err := r.client.WorkflowsNamed(ctx, name)
	if err != nil {
		return nil, fmt.Sprintf("Unable to look up workflow %q: %s", name, err)
	}
	switch len(ws) {
	case 0:
		return nil, fmt.Sprintf("No workflow named %q is visible to this login. Use the name shown in the console (Library > "+
			"Automation > Workflows).", name)
	case 1:
	default:
		return nil, fmt.Sprintf("%d workflows are named %q, so the name doesn't say which one to run.", len(ws), name)
	}
	if !strings.EqualFold(ws[0].Type, "operation") {
		return nil, fmt.Sprintf("Workflow %q is a %s workflow. Only operational workflows run on existing instances; a "+
			"provisioning workflow is given to the instance when it is made.", name, ws[0].Type)
	}
	return &ws[0], ""
}

// checkInputs compares the inputs with the workflow's form: every field must be one of its own, a
// password field goes in secret_inputs, and a required field with no default must be given.
func checkInputs(ctx context.Context, wf *client.Workflow, plan instanceWorkflowModel, resp *resource.ModifyPlanResponse) {
	open, secret := map[string]types.String{}, map[string]types.String{}
	if !plan.Inputs.IsUnknown() {
		resp.Diagnostics.Append(plan.Inputs.ElementsAs(ctx, &open, false)...)
	}
	if !plan.SecretInputs.IsUnknown() {
		resp.Diagnostics.Append(plan.SecretInputs.ElementsAs(ctx, &secret, false)...)
	}
	fields := map[string]client.WorkflowInput{}
	names := make([]string, 0, len(wf.Inputs))
	for _, in := range wf.Inputs {
		fields[in.FieldName] = in
		names = append(names, in.FieldName)
	}
	sort.Strings(names)
	known := "it has none"
	if len(names) > 0 {
		known = "its fields are " + strings.Join(names, ", ")
	}
	for _, where := range []struct {
		attr string
		m    map[string]types.String
	}{{"inputs", open}, {"secret_inputs", secret}} {
		keys := make([]string, 0, len(where.m))
		for k := range where.m {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			in, ok := fields[k]
			switch {
			case !ok:
				resp.Diagnostics.AddAttributeError(path.Root(where.attr).AtMapKey(k), "Unknown workflow input",
					fmt.Sprintf("Workflow %q has no field %q: %s.", wf.Name, k, known))
			case where.attr == "inputs" && in.Secret():
				resp.Diagnostics.AddAttributeError(path.Root("inputs").AtMapKey(k), "Secret in inputs",
					fmt.Sprintf("%q is a password field of workflow %q: put it in secret_inputs, so it is never shown.", k, wf.Name))
			}
		}
	}
	if plan.Inputs.IsUnknown() || plan.SecretInputs.IsUnknown() {
		return
	}
	for _, n := range names {
		in := fields[n]
		_, a := open[n]
		_, b := secret[n]
		if in.Required && in.Default == "" && !a && !b {
			where := "inputs"
			if in.Secret() {
				where = "secret_inputs"
			}
			resp.Diagnostics.AddAttributeError(path.Root(where), "Missing workflow input",
				fmt.Sprintf("Workflow %q needs %q (%s) in %s.", wf.Name, n, in.Label, where))
		}
	}
}

// run runs the workflow as planned and waits for it. The error names the platform's reason, with
// the secret values masked.
func (r *instanceWorkflowResource) run(ctx context.Context, m *instanceWorkflowModel) error {
	wf, msg := r.resolve(ctx, m.Workflow.ValueString())
	if msg != "" {
		return fmt.Errorf("%s", msg)
	}
	var ids []string
	if d := m.InstanceIDs.ElementsAs(ctx, &ids, false); d.HasError() {
		return fmt.Errorf("instance_ids can't be read")
	}
	insts := make([]int64, 0, len(ids))
	for _, s := range ids {
		n, _ := strconv.ParseInt(s, 10, 64)
		insts = append(insts, n)
	}
	slices.Sort(insts)
	inputs, secrets := map[string]string{}, []string{}
	open, secret := map[string]string{}, map[string]string{}
	if !m.Inputs.IsNull() {
		if d := m.Inputs.ElementsAs(ctx, &open, false); d.HasError() {
			return fmt.Errorf("inputs can't be read")
		}
	}
	if !m.SecretInputs.IsNull() {
		if d := m.SecretInputs.ElementsAs(ctx, &secret, false); d.HasError() {
			return fmt.Errorf("secret_inputs can't be read")
		}
	}
	for k, v := range open {
		inputs[k] = v
	}
	for k, v := range secret {
		inputs[k] = v
		secrets = append(secrets, v)
	}
	execID, err := r.client.RunWorkflow(ctx, wf.ID, insts, inputs)
	if err != nil {
		return fmt.Errorf("MTN Cloud refused to run workflow %q: %s", wf.Name, mask(err.Error(), secrets))
	}
	je, err := r.client.WaitForJobExecution(ctx, execID)
	m.WorkflowID, m.ExecutionID = types.Int64Value(wf.ID), types.Int64Value(execID)
	if err != nil {
		return fmt.Errorf("workflow %q (job execution %d) didn't finish: %s. It may still be running: see the console's "+
			"Activity, and apply again to run it again", wf.Name, execID, mask(err.Error(), secrets))
	}
	m.Status = types.StringValue(je.Status)
	if _, ok := client.RunDone(je.Status); !ok {
		why := r.client.ExecutionFailure(ctx, execID, secrets)
		if why == "" {
			why = "the run gave no reason"
			if no := r.withoutAgent(ctx, insts); len(no) > 0 {
				// Live QA 2026-11-01: a run on a VM made with install_agent = false failed after 4 minutes, with no output.
				why += fmt.Sprintf(". Instance %s has no MTN Cloud agent (install_agent = false), and a workflow reaches a VM "+
					"through its agent: make the VM with the agent, on a network behind an MTN Router with an external network",
					strings.Join(no, ", "))
			}
		}
		return fmt.Errorf("workflow %q ended %q (job execution %d): %s. The next apply runs it again", wf.Name, je.Status, execID, why)
	}
	return nil
}

// withoutAgent lists the instances, of those given, that were made without the agent. One that can't
// be read is left out: this only explains a failure.
func (r *instanceWorkflowResource) withoutAgent(ctx context.Context, ids []int64) []string {
	var out []string
	for _, id := range ids {
		if in, err := r.client.GetInstance(ctx, id); err == nil && in.NoAgent {
			out = append(out, strconv.FormatInt(id, 10))
		}
	}
	return out
}

func mask(s string, secrets []string) string {
	for _, v := range secrets {
		if v != "" {
			s = strings.ReplaceAll(s, v, "***")
		}
	}
	return s
}

func (r *instanceWorkflowResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	if !r.ready(&resp.Diagnostics) {
		return
	}
	var m instanceWorkflowModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &m)...)
	timeout, d := m.Timeouts.Create(ctx, workflowTimeout)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if err := r.run(ctx, &m); err != nil {
		// Nothing is kept: the next apply runs it again.
		resp.Diagnostics.AddError("Workflow run failed", err.Error())
		return
	}
	m.ID = types.StringValue(strconv.FormatInt(m.ExecutionID.ValueInt64(), 10))
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

// Read keeps what the last run left: a run isn't an object to read back.
func (r *instanceWorkflowResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
}

func (r *instanceWorkflowResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var m, state instanceWorkflowModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &m)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	m.ID = state.ID
	if sameRun(m, state) {
		m.WorkflowID, m.ExecutionID, m.Status = state.WorkflowID, state.ExecutionID, state.Status
		resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
		return
	}
	if !r.ready(&resp.Diagnostics) {
		return
	}
	timeout, d := m.Timeouts.Update(ctx, workflowTimeout)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if err := r.run(ctx, &m); err != nil {
		resp.Diagnostics.AddError("Workflow run failed", err.Error())
		// The previous run's values stay, so the plan still shows this change and the next apply runs it again.
		resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

// Delete undoes nothing: a run can't be taken back.
func (r *instanceWorkflowResource) Delete(context.Context, resource.DeleteRequest, *resource.DeleteResponse) {
}
