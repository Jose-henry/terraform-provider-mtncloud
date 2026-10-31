package storage

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-framework-timeouts/resource/timeouts"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"

	"github.com/mtn/terraform-provider-mtncloud/internal/client"
)

// accessTimeout bounds a MOS key order: MTN's workflow makes the keys in a few minutes.
const accessTimeout = 30 * time.Minute

// mos_access holds the tenant's MOS keys (B-032, owner 2026-10-31): it adopts the keys the tenant
// has, orders them only when it has none, and rotates them only when asked (rotate at create, or a
// changed rotate_trigger). Ordering rotates the tenant-wide keys (D-007), so never by accident.
type accessResource struct{ base }

type accessModel struct {
	ID              types.String   `tfsdk:"id"`
	Rotate          types.Bool     `tfsdk:"rotate"`
	RotateTrigger   types.String   `tfsdk:"rotate_trigger"`
	Source          types.String   `tfsdk:"source"`
	OrderID         types.String   `tfsdk:"order_id"`
	PublicEndpoint  types.String   `tfsdk:"public_endpoint"`
	PrivateEndpoint types.String   `tfsdk:"private_endpoint"`
	Timeouts        timeouts.Value `tfsdk:"timeouts"`
}

func newAccess() resource.Resource { return &accessResource{} }

var (
	_ resource.ResourceWithImportState = (*accessResource)(nil)
	_ resource.ResourceWithModifyPlan  = (*accessResource)(nil)
)

func (r *accessResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_mos_access"
}

func (r *accessResource) Schema(ctx context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	keep := []planmodifier.String{stringplanmodifier.UseStateForUnknown()}
	resp.Schema = schema.Schema{
		Description: "The tenant's MOS (MTN Object Storage) keys. MOS has one pair of keys per tenant, kept in Cypher. " +
			"Create **adopts** the keys the tenant already has and orders nothing; it orders the MOS catalog item only " +
			"when the tenant has no keys, or when `rotate = true`. Changing `rotate_trigger` orders new keys in place. " +
			"**Ordering replaces the keys for the whole tenant:** every tool and user of the old keys loses access " +
			"until it takes the new ones from Cypher (MTN's workflow updates the console's own bucket records). The " +
			"keys are never stored in state: MOS resources read them from Cypher. Destroy deletes nothing: the keys " +
			"stay, since others use them.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{Computed: true, PlanModifiers: keep,
				Description: "Where the keys are in Cypher: `secret/MOS-<tenant>_<user>_` (no secret)."},
			"rotate": schema.BoolAttribute{Optional: true,
				Description: "Order new keys at create even when the tenant has keys, which replaces them for the whole " +
					"tenant. Only used at create; to rotate later, change `rotate_trigger`. Defaults to `false`: adopt."},
			"rotate_trigger": schema.StringAttribute{Optional: true,
				Description: "Any value. Changing it from one value to another, once the resource exists, orders new keys " +
					"in place, replacing them for the whole tenant (for example a date: `rotate_trigger = \"2026-11\"`). " +
					"Setting it where it was unset (at create, or after an import) and removing it rotate nothing. A value " +
					"known only at apply may rotate: the plan says so."},
			"source": schema.StringAttribute{Computed: true, PlanModifiers: keep,
				Description: "`adopted` (the tenant's keys, nothing ordered) or `ordered` (this resource ordered them last)."},
			"order_id": schema.StringAttribute{Computed: true, PlanModifiers: keep,
				Description: "The catalog order that made the keys, when this resource ordered them; empty when adopted."},
			"public_endpoint":  schema.StringAttribute{Computed: true, PlanModifiers: keep, Description: "MOS endpoint from the internet."},
			"private_endpoint": schema.StringAttribute{Computed: true, PlanModifiers: keep, Description: "MOS endpoint from inside MTN Cloud."},
		},
		Blocks: map[string]schema.Block{"timeouts": timeouts.Block(ctx, timeouts.Opts{Create: true, Update: true})},
	}
}

// rotating reports whether an update orders new keys: rotate_trigger changed from one value to
// another. Setting it where it was unset (create, import) or removing it rotates nothing: a
// rotation cuts off every user of the tenant's keys, so it takes a deliberate new value.
func rotating(plan, state accessModel) bool {
	p, s := plan.RotateTrigger, state.RotateTrigger
	return !p.IsNull() && !p.IsUnknown() && !s.IsNull() && !s.IsUnknown() && p.ValueString() != s.ValueString()
}

// mayRotate reports whether the plan can't tell yet: a trigger known only at apply, where one was set.
func mayRotate(plan, state accessModel) bool {
	return plan.RotateTrigger.IsUnknown() && !state.RotateTrigger.IsNull()
}

// ModifyPlan warns before a rotation and plans what it changes.
func (r *accessResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.Plan.Raw.IsNull() {
		resp.Diagnostics.AddWarning("MOS keys stay",
			"Destroying mtncloud_mos_access deletes nothing: the tenant's MOS keys stay in Cypher, since other tools and users rely on them.")
		return
	}
	var plan accessModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if req.State.Raw.IsNull() {
		if plan.Rotate.ValueBool() {
			resp.Diagnostics.AddWarning("MOS keys will be rotated",
				"rotate = true: if the tenant already has MOS keys, this apply orders new ones, which replaces them for the whole tenant.")
		}
		return
	}
	var state accessModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	switch {
	case rotating(plan, state):
		resp.Diagnostics.AddWarning("MOS keys will be rotated",
			"rotate_trigger changed: this apply orders new MOS keys, which replaces them for the whole tenant. Every tool and user "+
				"of the old keys loses access until it takes the new ones from Cypher.")
	case mayRotate(plan, state):
		resp.Diagnostics.AddWarning("MOS keys may be rotated",
			"rotate_trigger is known only at apply: if it differs from "+strconv.Quote(state.RotateTrigger.ValueString())+
				", this apply orders new MOS keys, which replaces them for the whole tenant.")
	default:
		return
	}
	for _, a := range []string{"id", "source", "order_id", "public_endpoint", "private_endpoint"} {
		resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root(a), types.StringUnknown())...)
	}
}

func (r *accessResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	if !r.ready(&resp.Diagnostics) {
		return
	}
	var m accessModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &m)...)
	timeout, d := m.Timeouts.Create(ctx, accessTimeout)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	keys, err := r.client.MOSKeys(ctx)
	if err != nil {
		resp.Diagnostics.AddError("Unable to read the MOS keys", err.Error())
		return
	}
	m.OrderID, m.Source = types.StringValue(""), types.StringValue("adopted")
	if keys == nil || m.Rotate.ValueBool() {
		old := keys
		if keys, err = r.order(ctx, &m); err != nil {
			if !r.pending(ctx, err, &m, old, &resp.Diagnostics) {
				resp.Diagnostics.AddError("Unable to order MOS keys", err.Error())
				return
			}
			resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
			return
		}
		if m.Rotate.ValueBool() {
			resp.Diagnostics.AddWarning("MOS keys rotated", rotatedNote(m.OrderID.ValueString()))
		}
	}
	setKeys(&m, keys)
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

// order orders new keys, waits for MTN's workflow, and reads where they are. Cached keys are
// dropped, so MOS resources later in the same apply use the new ones.
func (r *accessResource) order(ctx context.Context, m *accessModel) (*client.MOSKeySet, error) {
	item, err := r.client.OrderMOSKeys(ctx)
	if err != nil {
		return nil, err
	}
	m.OrderID, m.Source = types.StringValue(strconv.FormatInt(item.ID, 10)), types.StringValue("ordered")
	got, err := r.client.WaitForExecution(ctx, item.ID)
	if got != nil {
		item = got
	}
	if done, ok := client.ExecutionDone(item.Execution); (done && !ok) || strings.EqualFold(item.Status, "failed") {
		why := ""
		if item.Execution != nil {
			if f := r.client.ExecutionFailure(ctx, item.Execution.ID, nil); f != "" {
				why = " The workflow says: " + strings.TrimRight(f, ". ") + "."
			}
		}
		return nil, fmt.Errorf("order %d failed.%s Check it under Catalog > Inventory", item.ID, why)
	}
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			err = errors.New("it hasn't finished before the timeout")
		}
		return nil, &pendingError{fmt.Errorf("order %d was placed, but its workflow hasn't finished: %w", item.ID, err)}
	}
	r.client.ForgetMOS()
	var keys *client.MOSKeySet
	if err := r.client.Await(ctx, func(ctx context.Context) (bool, error) {
		var err error
		keys, err = r.client.MOSKeys(ctx)
		return keys != nil, err
	}); err != nil {
		return nil, &pendingError{fmt.Errorf("order %d succeeded, but no MOS keys are in Cypher yet (%w)", item.ID, err)}
	}
	return keys, nil
}

// pendingError is an order that was placed but isn't finished: ordering again would rotate the keys
// a second time, so it is saved, not failed (rule 11).
type pendingError struct{ error }

func (e *pendingError) Unwrap() error { return e.error }

const pendingPrefix = "order-"

// pending saves a placed, unfinished order in m with a warning, and reports whether err was one.
// The ID is the keys' place in Cypher once known, else "order-<id>" until a refresh finds them. It
// isn't an error: that would taint the resource, and its replacement would order again.
func (r *accessResource) pending(ctx context.Context, err error, m *accessModel, old *client.MOSKeySet, diags *diag.Diagnostics) bool {
	var pe *pendingError
	if !errors.As(err, &pe) {
		return false
	}
	if old != nil {
		setKeys(m, old)
	} else {
		m.ID = types.StringValue(pendingPrefix + m.OrderID.ValueString())
		m.PublicEndpoint, m.PrivateEndpoint = types.StringValue(""), types.StringValue("")
	}
	diags.AddWarning("MOS keys not ready yet", pe.Error()+". The order is saved in state and is not placed again: "+
		"a later refresh (terraform plan or apply) reads the new keys from Cypher once MTN's workflow has made them. "+
		"Check the order under Catalog > Inventory.")
	tflog.Warn(ctx, "MOS key order not finished", map[string]any{"order_id": m.OrderID.ValueString()})
	return true
}

func rotatedNote(orderID string) string {
	return "Order " + orderID + " made new MOS keys for the whole tenant. Every tool and user of the old keys must take the " +
		"new ones from Cypher; MTN's workflow updates the console's bucket records itself."
}

func setKeys(m *accessModel, k *client.MOSKeySet) {
	m.ID = types.StringValue(k.Prefix)
	m.PublicEndpoint, m.PrivateEndpoint = types.StringValue(k.PublicEndpoint), types.StringValue(k.PrivateEndpoint)
}

func (r *accessResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	if !r.ready(&resp.Diagnostics) {
		return
	}
	var m accessModel
	resp.Diagnostics.Append(req.State.Get(ctx, &m)...)
	keys, err := r.client.MOSKeys(ctx)
	if err != nil {
		resp.Diagnostics.AddError("Unable to read the MOS keys", err.Error())
		return
	}
	if keys == nil && strings.HasPrefix(m.ID.ValueString(), pendingPrefix) {
		resp.Diagnostics.AddWarning("MOS keys not ready yet", "Order "+m.OrderID.ValueString()+
			" hasn't put MOS keys in Cypher yet. Check it under Catalog > Inventory.")
		resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
		return
	}
	if keys == nil {
		// Gone outside Terraform: the next apply orders keys, since the tenant has none (rule 8).
		resp.State.RemoveResource(ctx)
		return
	}
	setKeys(&m, keys)
	if m.Source.IsNull() {
		m.Source, m.OrderID = types.StringValue("adopted"), types.StringValue("") // imported
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

func (r *accessResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	if !r.ready(&resp.Diagnostics) {
		return
	}
	var plan, state accessModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if !rotating(plan, state) {
		// rotate only counts at create: nothing to send.
		plan.ID, plan.Source, plan.OrderID = state.ID, state.Source, state.OrderID
		plan.PublicEndpoint, plan.PrivateEndpoint = state.PublicEndpoint, state.PrivateEndpoint
		resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
		return
	}
	timeout, d := plan.Timeouts.Update(ctx, accessTimeout)
	resp.Diagnostics.Append(d...)
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	keys, err := r.order(ctx, &plan)
	if err != nil {
		old := &client.MOSKeySet{Prefix: state.ID.ValueString(), PublicEndpoint: state.PublicEndpoint.ValueString(),
			PrivateEndpoint: state.PrivateEndpoint.ValueString()}
		if !r.pending(ctx, err, &plan, old, &resp.Diagnostics) {
			resp.Diagnostics.AddError("Unable to rotate the MOS keys", err.Error())
			return
		}
		resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
		return
	}
	setKeys(&plan, keys)
	resp.Diagnostics.AddWarning("MOS keys rotated", rotatedNote(plan.OrderID.ValueString()))
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Delete deletes nothing: the keys are the tenant's, and other tools and users rely on them.
func (r *accessResource) Delete(context.Context, resource.DeleteRequest, *resource.DeleteResponse) {}

// ImportState adopts the tenant's keys: any ID (for example `mos`) works, as a tenant has one pair.
func (r *accessResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}
