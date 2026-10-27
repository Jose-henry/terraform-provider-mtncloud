package catalog

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-framework-timeouts/resource/timeouts"
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

// projectType is the catalog item MTN's own "Create Project" workflow is ordered by.
const projectType = "Create Project"

// The workflow usually finishes within a minute; this bounds one that never reports.
const projectTimeout = 15 * time.Minute

// projectName is the catalog form's own rule for the name (its verifyPattern, captured 2026-10-27).
var projectName = regexp.MustCompile(`^[a-z][a-z0-9_-]*$`)

var (
	_ resource.ResourceWithConfigure   = (*projectResource)(nil)
	_ resource.ResourceWithImportState = (*projectResource)(nil)
	_ resource.ResourceWithModifyPlan  = (*projectResource)(nil)
)

func newProject() resource.Resource { return &projectResource{} }

type projectResource struct{ base }

type projectModel struct {
	ID          types.String   `tfsdk:"id"`
	Name        types.String   `tfsdk:"name"`
	Description types.String   `tfsdk:"description"`
	FullName    types.String   `tfsdk:"full_name"`
	Status      types.String   `tfsdk:"status"`
	Timeouts    timeouts.Value `tfsdk:"timeouts"`
}

func (r *projectResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_project"
}

func (r *projectResource) Schema(ctx context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "An MTN Cloud project, ordered from the catalog (\"Create Project\"). Creating it waits for the order's " +
			"workflow to finish, not for the project to sync in: its default security group, networks and floating " +
			"IP pools can take up to an hour to appear. A project can't be renamed or deleted from Terraform.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:      true,
				Description:   "ID of the catalog order item (Catalog > Inventory in the console). Used to import the project.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"name": schema.StringAttribute{
				Required: true,
				Description: "Project name: a lowercase letter, then lowercase letters, digits, `_` or `-`. MTN Cloud adds " +
					"your tenant name (see `full_name`). It can't be changed after the order.",
				Validators: []validator.String{stringvalidator.RegexMatches(projectName,
					"must begin with a lowercase letter, followed only by lowercase letters, numbers, underscores or hyphens")},
			},
			"description": schema.StringAttribute{
				Optional:    true,
				Description: "Description of the project. It can't be changed after the order.",
			},
			"full_name": schema.StringAttribute{
				Computed: true,
				Description: "The name MTN Cloud gives the project, `<name>-<tenant>`. Other resources accept either " +
					"name in their `project` argument.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"status": schema.StringAttribute{
				Computed:    true,
				Description: "Status of the order's workflow, for example `success`.",
			},
		},
		Blocks: map[string]schema.Block{
			"timeouts": timeouts.Block(ctx, timeouts.Opts{Create: true}),
		},
	}
}

// ModifyPlan refuses a change to an ordered project (owner, 2026-10-27: no second project is ever
// ordered by accident), and refuses a new one whose name is taken.
func (r *projectResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.Plan.Raw.IsNull() || r.client == nil {
		return // destroy (Delete explains), or an unconfigured provider
	}
	var plan, state projectModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if !req.State.Raw.IsNull() {
		resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	}
	if resp.Diagnostics.HasError() {
		return
	}
	if !req.State.Raw.IsNull() && !state.Name.IsNull() {
		// Imported projects have no name in state yet; the configuration supplies it.
		changed := func(attr string, from, to types.String) {
			if to.IsUnknown() || from.ValueString() == to.ValueString() {
				return
			}
			fix := fmt.Sprintf("Set it back to %q.", from.ValueString())
			if from.IsNull() {
				fix = fmt.Sprintf("Remove %s: the project was ordered without one.", attr)
			}
			resp.Diagnostics.AddAttributeError(path.Root(attr), "A project can't be changed",
				fmt.Sprintf("Project %s was ordered with a different %s. MTN Cloud can't change it, and Terraform won't "+
					"order a second project. %s", state.FullName.ValueString(), attr, fix))
		}
		changed("name", state.Name, plan.Name)
		changed("description", state.Description, plan.Description)
		return
	}
	if plan.Name.IsUnknown() {
		return
	}
	who, err := r.client.WhoAmI(ctx)
	if err != nil {
		resp.Diagnostics.AddError("Unable to read your tenant", err.Error())
		return
	}
	full := plan.Name.ValueString() + "-" + who.AccountName
	resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("full_name"), full)...)
	projects, err := r.client.Projects(ctx)
	if err != nil {
		resp.Diagnostics.AddError("Unable to list your projects", err.Error())
		return
	}
	exists := slices.ContainsFunc(projects, func(p client.Option) bool { return strings.EqualFold(p.Name, full) })
	switch {
	case !req.State.Raw.IsNull() && !exists:
		// An import adopting its configuration: the name must be the one it was ordered with.
		resp.Diagnostics.AddAttributeError(path.Root("name"), "No such project",
			fmt.Sprintf("Order %s is being given the name %q, but there is no project %s. Set name to the name the "+
				"project was ordered with (its full name without the -%s suffix).", state.ID.ValueString(),
				plan.Name.ValueString(), full, who.AccountName))
	case req.State.Raw.IsNull() && exists:
		// Only a synced project shows here: an order still running (up to an hour) doesn't.
		resp.Diagnostics.AddAttributeError(path.Root("name"), "Project already exists",
			fmt.Sprintf("Project %s already exists. Pick another name, or import it with its order ID "+
				"(Catalog > Inventory): terraform import <address> <id>.", full))
	}
}

func (r *projectResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	if !r.ready(&resp.Diagnostics) {
		return
	}
	var plan projectModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	timeout, d := plan.Timeouts.Create(ctx, projectTimeout)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	p, err := r.client.Placement(ctx)
	if err != nil {
		resp.Diagnostics.AddError("Unable to find your group and cloud", err.Error())
		return
	}
	// The catalog form's fields; MTN's workflow names the pool "<projectName>-<tenant>" in the cloud.
	item, err := r.client.OrderWorkflow(ctx, projectType, map[string]any{
		"groupName":          strconv.FormatInt(p.GroupID, 10),
		"cloud":              strconv.FormatInt(p.CloudID, 10),
		"projectName":        plan.Name.ValueString(),
		"projectDescription": plan.Description.ValueString(),
	})
	if err != nil {
		msg := err.Error()
		if !client.IsClientError(err) {
			// Anything but a refusal (a timeout, a lost connection, a 5xx) may have placed the order, and
			// the plan-time name check can't see an order until it has synced, up to an hour later.
			msg += ". The order may have been placed. Before applying again, check Catalog > Inventory in the " +
				"console: if it is there, import it (terraform import <address> <id>). Applying again now may order " +
				"a second project."
		}
		resp.Diagnostics.AddError("Unable to order the project", msg)
		return
	}
	plan.ID = types.StringValue(strconv.FormatInt(item.ID, 10))
	got, err := r.client.WaitForExecution(ctx, item.ID)
	if got != nil {
		item = got
	}
	done, ok := client.ExecutionDone(item.Execution)
	switch {
	case done && !ok, strings.EqualFold(item.Status, "failed"):
		// Nothing was made, so nothing is kept: the next apply orders again.
		resp.Diagnostics.AddError("Project order failed",
			fmt.Sprintf("The %q workflow for %s ended with %q%s. Check the order (ID %d) under Catalog > Inventory.",
				projectType, plan.FullName.ValueString(), executionStatus(item), message(item), item.ID))
		return
	case err != nil:
		// The order is placed: keep it, so a retry never orders a second project.
		detail := err.Error()
		switch {
		case got == nil:
			detail = fmt.Sprintf("order item %d was never found in the inventory", item.ID)
		case errors.Is(err, context.DeadlineExceeded):
			detail = "it hasn't reported a result yet"
		}
		resp.Diagnostics.AddWarning("Project ordered, result not known yet",
			fmt.Sprintf("Order %d for %s was placed, but its workflow's result isn't known: %s. Check it under "+
				"Catalog > Inventory; the next refresh reads it again.", item.ID, plan.FullName.ValueString(), detail))
	}
	plan.Status = types.StringValue(executionStatus(item))
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func executionStatus(it *client.OrderedItem) string {
	if it.Execution != nil && it.Execution.Status != "" {
		return strings.ToLower(it.Execution.Status)
	}
	return strings.ToLower(it.Status)
}

func message(it *client.OrderedItem) string {
	if it.StatusMessage == "" {
		return ""
	}
	return ": " + it.StatusMessage
}

func (r *projectResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	if !r.ready(&resp.Diagnostics) {
		return
	}
	var state projectModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	id, err := strconv.ParseInt(state.ID.ValueString(), 10, 64)
	if err != nil {
		resp.Diagnostics.AddError("Invalid project ID", fmt.Sprintf("%q is not a catalog order ID: %s", state.ID.ValueString(), err))
		return
	}
	item, err := r.client.GetOrderedItem(ctx, id)
	if client.IsNotFound(err) {
		if state.Status.ValueString() == "ordered" {
			// Never confirmed: dropping it would order a second project. Keep it until someone checks.
			resp.Diagnostics.AddWarning("Project order not found",
				fmt.Sprintf("Order %d for %s isn't in the inventory. Check Catalog > Inventory: if the project exists, "+
					"import it by its order ID; if not, remove this resource from state (terraform state rm) to order again.",
					id, state.FullName.ValueString()))
			return
		}
		resp.State.RemoveResource(ctx) // the order is gone from the inventory
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Unable to read the project order", err.Error())
		return
	}
	if item.TypeName != projectType {
		resp.Diagnostics.AddError("Not a project order",
			fmt.Sprintf("Catalog order %d is a %q, not a %q. Import the project by its own order ID.", id, item.TypeName, projectType))
		return
	}
	if done, ok := client.ExecutionDone(item.Execution); (done && !ok) || strings.EqualFold(item.Status, "failed") {
		// It failed after Create stopped waiting: nothing was made, so the next apply orders again.
		resp.Diagnostics.AddWarning("Project order failed",
			fmt.Sprintf("The %q workflow for %s (order %d) ended with %q%s. The next apply orders it again.",
				projectType, state.FullName.ValueString(), id, executionStatus(item), message(item)))
		resp.State.RemoveResource(ctx)
		return
	}
	// The inventory doesn't show the order's form, so name and description stay as ordered.
	state.Status = types.StringValue(executionStatus(item))
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Update runs only when an imported project adopts its configuration's name and description
// (ModifyPlan refuses every other change): nothing is sent.
func (r *projectResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state projectModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	plan.Status = state.Status
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Delete refuses: MTN Cloud has no "delete project" order (owner, 2026-10-27).
func (r *projectResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state projectModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	resp.Diagnostics.AddError("A project can't be deleted from Terraform",
		fmt.Sprintf("MTN Cloud has no way to delete project %s from Terraform. To stop managing it, replace the "+
			"resource block with a removed block (lifecycle { destroy = false }) or run terraform state rm. To delete "+
			"the project itself, ask MTN Cloud support.", state.FullName.ValueString()))
}

func (r *projectResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	if _, err := strconv.ParseInt(req.ID, 10, 64); err != nil {
		resp.Diagnostics.AddError("Invalid import ID",
			fmt.Sprintf("Import a project by its catalog order ID (Catalog > Inventory), for example 9801. Got %q.", req.ID))
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
}
