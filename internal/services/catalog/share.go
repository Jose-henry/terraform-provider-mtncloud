package catalog

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-framework-timeouts/resource/timeouts"
	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64default"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/mtn/terraform-provider-mtncloud/internal/client"
)

const shareTimeout = 30 * time.Minute

// shareResource is an NFS or SMB share. Unlike other catalog items, its order form also updates
// and deletes it (updateOnly / deleteOnly), so it has a real update and destroy (D-028). MTN Cloud
// has no API that reads a share back: state holds what was last ordered.
type shareResource struct {
	base
	item string // "MTN NFS" or "MTN SMB"
	smb  bool
}

func newNFSShare() resource.Resource { return &shareResource{item: "MTN NFS"} }
func newSMBShare() resource.Resource { return &shareResource{item: "MTN SMB", smb: true} }

type shareModel struct {
	ID              types.String   `tfsdk:"id"`
	Name            types.String   `tfsdk:"name"`
	QuotaGB         types.Int64    `tfsdk:"quota_gb"`
	ClientIPs       types.List     `tfsdk:"client_ips"`
	Password        types.String   `tfsdk:"password"`
	PasswordVersion types.Int64    `tfsdk:"password_version"`
	Status          types.String   `tfsdk:"status"`
	Timeouts        timeouts.Value `tfsdk:"timeouts"`
}

func (r *shareResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	if r.smb {
		resp.TypeName = req.ProviderTypeName + "_smb_share"
		return
	}
	resp.TypeName = req.ProviderTypeName + "_nfs_share"
}

func (r *shareResource) Schema(ctx context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	kind := "NFS"
	if r.smb {
		kind = "SMB"
	}
	keep := []planmodifier.String{stringplanmodifier.UseStateForUnknown()}
	attrs := map[string]schema.Attribute{
		"id": schema.StringAttribute{Computed: true, Description: "ID of the order that created the share.", PlanModifiers: keep},
		"name": schema.StringAttribute{Required: true, Description: "Share (volume) name. Changing it makes a new share.",
			// An imported share has no name in state yet: adopting one isn't a new share.
			PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplaceIf(
				func(_ context.Context, req planmodifier.StringRequest, resp *stringplanmodifier.RequiresReplaceIfFuncResponse) {
					resp.RequiresReplace = !req.StateValue.IsNull()
				}, "Changing the name makes a new share.", "Changing the name makes a new share.")},
			Validators: []validator.String{stringvalidator.RegexMatches(regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`), "letters, digits, `.`, `_` and `-`")}},
		"quota_gb": schema.Int64Attribute{Optional: true, Computed: true, Default: int64default.StaticInt64(10),
			Description: "Size limit in GB. Defaults to 10. Changing it updates the share in place.",
			Validators:  []validator.Int64{int64validator.AtLeast(1)}},
		"client_ips": schema.ListAttribute{Optional: true, ElementType: types.StringType,
			Description: "Addresses allowed to mount the share. Left out or empty, every address may. Changing it updates the share in place."},
		"status": schema.StringAttribute{Computed: true, Description: "Result of the last order, for example `success`."},
	}
	if r.smb {
		attrs["password"] = schema.StringAttribute{Optional: true, Sensitive: true, WriteOnly: true,
			Description: "Password of the share's user. Write-only (Terraform 1.11 or newer): never stored. To change it, change `password_version` too."}
		attrs["password_version"] = schema.Int64Attribute{Optional: true,
			Description: "Any number; changing it sends `password` again. Write-only values can't be compared, so this is how a new password is noticed."}
	} else {
		attrs["password"] = schema.StringAttribute{Computed: true, Description: "Not used by NFS shares.", PlanModifiers: keep}
		attrs["password_version"] = schema.Int64Attribute{Computed: true, Description: "Not used by NFS shares."}
	}
	resp.Schema = schema.Schema{
		Description: "An " + kind + " file share, ordered from MTN Cloud's catalog (" + r.item + "). Quota and client changes are ordered as " +
			"updates; destroy orders its deletion. MTN Cloud doesn't report a share's settings back, so changes made in the console aren't seen.",
		Attributes: attrs,
		Blocks:     map[string]schema.Block{"timeouts": timeouts.Block(ctx, timeouts.Opts{Create: true, Update: true, Delete: true})},
	}
}

// order places one share order and waits for its result. A failure is an error, with the
// workflow's own message; an unknown result is a warning. placed runs as soon as the order is placed, before the wait.
func (r *shareResource) order(ctx context.Context, config map[string]any, what string, diags *diag.Diagnostics, placed func(int64)) *client.OrderedItem {
	item, err := r.client.OrderWorkflow(ctx, r.item, config)
	if err != nil {
		diags.AddError("Unable to "+what, err.Error())
		return nil
	}
	if placed != nil {
		placed(item.ID)
	}
	got, err := r.client.WaitForExecution(ctx, item.ID)
	if got != nil {
		item = got
	}
	if done, ok := client.ExecutionDone(item.Execution); (done && !ok) || strings.EqualFold(item.Status, "failed") {
		diags.AddError("Unable to "+what, fmt.Sprintf("Order %d ended with %q%s. Check it under Catalog > Inventory.", item.ID, executionStatus(item), message(item)))
		return nil
	}
	if err != nil {
		// Kept with a warning: placing the order again could make a second share.
		diags.AddWarning("The share's order result isn't known", fmt.Sprintf("Order %d was placed, but its result isn't known: %s. "+
			"Check it under Catalog > Inventory.", item.ID, err))
	}
	return item
}

func (r *shareResource) config(ctx context.Context, m shareModel, password types.String, diags *diag.Diagnostics) map[string]any {
	cfg := map[string]any{"volumeName": m.Name.ValueString(), "quotaSize": m.QuotaGB.ValueInt64() << 30}
	var ips []string
	diags.Append(m.ClientIPs.ElementsAs(ctx, &ips, false)...)
	if len(ips) > 0 {
		cfg["clientIps"] = strings.Join(ips, ",")
	}
	if r.smb && password.ValueString() != "" {
		cfg["smbPassword"] = password.ValueString()
	}
	return cfg
}

func (r *shareResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	if !r.ready(&resp.Diagnostics) {
		return
	}
	var m shareModel
	var pw types.String
	resp.Diagnostics.Append(req.Plan.Get(ctx, &m)...)
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("password"), &pw)...)
	timeout, d := m.Timeouts.Create(ctx, shareTimeout)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cfg := r.config(ctx, m, pw, &resp.Diagnostics)
	cfg["updateOnly"], cfg["deleteOnly"] = false, false
	if !r.smb {
		m.PasswordVersion = types.Int64Null()
	}
	// Saved as soon as the order is placed, so a timeout never leads to a second order. A failed
	// workflow drops it: nothing was made, and the next apply orders again.
	m.Password, m.Status = types.StringNull(), types.StringValue("ordered")
	item := r.order(ctx, cfg, "create the share", &resp.Diagnostics, func(id int64) {
		m.ID = types.StringValue(strconv.FormatInt(id, 10))
		resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
	})
	if item == nil {
		resp.State.RemoveResource(ctx)
		return
	}
	m.Status = types.StringValue(executionStatus(item))
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

// Read keeps state: no API reads a share. It drops the share only when its order is gone.
func (r *shareResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	if !r.ready(&resp.Diagnostics) {
		return
	}
	var m shareModel
	resp.Diagnostics.Append(req.State.Get(ctx, &m)...)
	id, err := strconv.ParseInt(m.ID.ValueString(), 10, 64)
	if err != nil {
		resp.Diagnostics.AddError("Invalid share ID", fmt.Sprintf("%q is not a catalog order ID.", m.ID.ValueString()))
		return
	}
	item, err := r.client.GetOrderedItem(ctx, id)
	if client.IsNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Unable to read the share's order", err.Error())
		return
	}
	if item.TypeName != r.item {
		resp.Diagnostics.AddError("Wrong catalog item", fmt.Sprintf("Order %d is a %q, not a %q.", id, item.TypeName, r.item))
		return
	}
	if m.QuotaGB.IsNull() {
		m.QuotaGB = types.Int64Value(10) // imported: the order's form isn't readable
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

func (r *shareResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	if !r.ready(&resp.Diagnostics) {
		return
	}
	var plan, state shareModel
	var pw types.String
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("password"), &pw)...)
	if !r.smb {
		plan.PasswordVersion = types.Int64Null()
	}
	timeout, d := plan.Timeouts.Update(ctx, shareTimeout)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	quota := !plan.QuotaGB.Equal(state.QuotaGB)
	clients := !plan.ClientIPs.Equal(state.ClientIPs) && (len(plan.ClientIPs.Elements()) != 0 || len(state.ClientIPs.Elements()) != 0)
	password := r.smb && !plan.PasswordVersion.Equal(state.PasswordVersion) && pw.ValueString() != ""
	if state.ID.IsNull() || state.Name.IsNull() || (!quota && !clients && !password) {
		// An import adopting its configuration, or nothing the share's order changes.
		plan.ID, plan.Status, plan.Password = state.ID, state.Status, types.StringNull()
		resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
		return
	}
	cfg := r.config(ctx, plan, pw, &resp.Diagnostics)
	cfg["updateOnly"], cfg["deleteOnly"] = true, false
	cfg["updateQuota"], cfg["updateClientIps"] = quota, clients
	if r.smb {
		cfg["updatePassword"] = password
		if !password {
			delete(cfg, "smbPassword")
		}
	}
	if clients {
		mode := "Replace"
		if len(plan.ClientIPs.Elements()) == 0 {
			mode = "Allow All"
		}
		form, err := r.client.CatalogForm(ctx, r.item)
		if err != nil {
			resp.Diagnostics.AddError("Unable to read the "+r.item+" order form", err.Error())
			return
		}
		c, err := r.client.FormChoice(ctx, form.Fields["updateType"], "client update type", mode)
		if err != nil {
			resp.Diagnostics.AddError("Unable to update the share's clients", err.Error())
			return
		}
		cfg["updateType"] = c.Value
	}
	item := r.order(ctx, cfg, "update the share", &resp.Diagnostics, nil)
	if item == nil {
		return
	}
	plan.ID, plan.Status, plan.Password = state.ID, types.StringValue(executionStatus(item)), types.StringNull()
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *shareResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	if !r.ready(&resp.Diagnostics) {
		return
	}
	var m shareModel
	resp.Diagnostics.Append(req.State.Get(ctx, &m)...)
	timeout, d := m.Timeouts.Delete(ctx, shareTimeout)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	r.order(ctx, map[string]any{"volumeName": m.Name.ValueString(), "deleteOnly": true, "updateOnly": false}, "delete the share", &resp.Diagnostics, nil)
}

func (r *shareResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	if _, err := strconv.ParseInt(req.ID, 10, 64); err != nil {
		resp.Diagnostics.AddError("Invalid import ID",
			fmt.Sprintf("Import a share by the ID of the order that created it (Catalog > Inventory). Got %q.", req.ID))
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
}
