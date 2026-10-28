package catalog

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-framework-timeouts/resource/timeouts"
	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/listvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/listplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/mtn/terraform-provider-mtncloud/internal/client"
	"github.com/mtn/terraform-provider-mtncloud/internal/planmodifiers"
)

const orderTimeout = 90 * time.Minute

var (
	_ resource.ResourceWithModifyPlan  = (*orderResource)(nil)
	_ resource.ResourceWithImportState = (*orderResource)(nil)
)

// orderResource is one catalog item, ordered (D-026). Its inputs can't change after the order:
// nothing re-runs it. Destroy refuses, as for projects (D-028): what an order made is deleted in
// the console, under Apps or Instances.
type orderResource struct {
	base
	spec orderSpec
}

func orderResources() []func() resource.Resource {
	out := []func() resource.Resource{}
	for _, s := range orderSpecs() {
		out = append(out, func() resource.Resource { return &orderResource{spec: s} })
	}
	return out
}

func (r *orderResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_" + r.spec.typeName
}

// inputs lists every argument path with its field, the volume parts expanded.
func (r *orderResource) inputs() map[string]field {
	out := map[string]field{}
	if r.spec.target {
		out["instance_id"] = field{attr: "instance_id", kind: kText}
	}
	for _, f := range r.spec.fields {
		if f.kind == kVolumes {
			for _, a := range []string{"root_volume_size", "datastore", "data_volume"} {
				out[f.attr+a] = f
			}
			continue
		}
		out[f.attr] = f
	}
	return out
}

func (r *orderResource) Schema(ctx context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	where := "Instances"
	if r.spec.managed {
		where = "Apps"
	}
	desc := r.spec.about + " Ordered from MTN Cloud's catalog (" + r.spec.item + "). The order's inputs can't be changed " +
		"afterwards, and `terraform destroy` refuses: delete what it made in the console, under " + where +
		", then remove the resource from state. Secrets are write-only (Terraform 1.11 or newer): they are sent with " +
		"the order and never stored."
	if r.spec.target {
		desc = r.spec.about + " Ordered from MTN Cloud's catalog (" + r.spec.item + "). Its inputs can't be changed " +
			"afterwards, and `terraform destroy` refuses: the certificate stays on the instance; remove the resource from state."
	}
	keep := []planmodifier.String{stringplanmodifier.UseStateForUnknown()}
	attrs := map[string]schema.Attribute{
		"id":     schema.StringAttribute{Computed: true, Description: "Order ID (Catalog > Inventory).", PlanModifiers: keep},
		"status": schema.StringAttribute{Computed: true, Description: "The order's workflow result, for example `success`.", PlanModifiers: keep},
	}
	blocks := map[string]schema.Block{"timeouts": timeouts.Block(ctx, timeouts.Opts{Create: true})}
	if r.spec.target {
		attrs["instance_id"] = schema.StringAttribute{Required: true,
			Description: "ID of the instance to install the certificate on, for example `mtncloud_instance.web.id`."}
	} else {
		attrs["instance_ids"] = schema.ListAttribute{Computed: true, ElementType: types.StringType,
			Description:   "IDs of the instances the order created, found by name in the project.",
			PlanModifiers: []planmodifier.List{listplanmodifier.UseStateForUnknown()}}
	}
	for _, f := range r.spec.fields {
		switch f.kind {
		case kVolumes:
			attrs[f.attr+"root_volume_size"] = schema.Int64Attribute{Optional: !f.required, Required: f.required,
				Description: "Root disk size in GB.", Validators: []validator.Int64{int64validator.AtLeast(1)}}
			attrs[f.attr+"datastore"] = schema.StringAttribute{Optional: true,
				Description: "Datastore (volume type) of the root disk, by name. Left out, the project's default."}
			blocks[f.attr+"data_volume"] = schema.ListNestedBlock{
				Description: "Extra disks, in order.",
				NestedObject: schema.NestedBlockObject{Attributes: map[string]schema.Attribute{
					"size":      schema.Int64Attribute{Required: true, Description: "Size in GB.", Validators: []validator.Int64{int64validator.AtLeast(1)}},
					"datastore": schema.StringAttribute{Optional: true, Description: "Datastore, by name. Left out, the project's default."},
				}},
			}
		default:
			attrs[f.attr] = r.attribute(f)
		}
	}
	resp.Schema = schema.Schema{Description: desc, Attributes: attrs, Blocks: blocks}
}

func (r *orderResource) attribute(f field) schema.Attribute {
	d := f.desc
	if f.def != "" && !strings.Contains(d, "Defaults") {
		d += " Defaults to `" + f.def + "`."
	}
	// A required form field with a default is optional here: the default is sent, as the console does.
	req := f.required && f.def == ""
	opt := !req
	switch f.kind {
	case kGroup:
		// Optional+Computed: left out, the provider's group is used and kept in state (D-027).
		return schema.StringAttribute{Optional: true, Computed: true, PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			Description: "Group, by name. Defaults to the provider's `group` (or `MTNCLOUD_GROUP`)."}
	case kProject:
		// Optional+Computed: left out, the provider's project is used and kept in state (D-027).
		return schema.StringAttribute{Optional: true, Computed: true, PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			Description: "Project, by name. Defaults to the provider's `project` (or `MTNCLOUD_PROJECT`)."}
	case kNumber, kBytes:
		return schema.Int64Attribute{Optional: opt, Required: req, Description: d}
	case kFlag:
		return schema.BoolAttribute{Optional: opt, Required: req, Description: d}
	case kSecret:
		return schema.StringAttribute{Optional: opt, Required: req, Sensitive: true, WriteOnly: true,
			Description: d + " Write-only: sent with the order, never stored in state."}
	case kList, kLines, kNetworks:
		v := []validator.List{listvalidator.SizeAtLeast(1)}
		return schema.ListAttribute{Optional: opt, Required: req, ElementType: types.StringType, Description: d, Validators: v}
	case kSecGroups, kLabels:
		return schema.SetAttribute{Optional: opt, Required: req, ElementType: types.StringType, Description: d}
	}
	return schema.StringAttribute{Optional: opt, Required: req, Description: d}
}

// source is a plan, a state or a configuration.
type source interface {
	GetAttribute(context.Context, path.Path, any) diag.Diagnostics
}

// ModifyPlan fills the provider's group and project, refuses a change to an ordered item, and checks
// every name against MTN Cloud before anything is ordered.
func (r *orderResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.Plan.Raw.IsNull() || r.client == nil {
		return // destroy (Delete explains), or an unconfigured provider
	}
	d := r.client.Defaults()
	for _, f := range r.spec.fields {
		switch f.kind {
		case kGroup:
			resp.Diagnostics.Append(planmodifiers.FillDefault(ctx, req.Config, &resp.Plan, f.attr, "MTNCLOUD_GROUP", d.Group)...)
		case kProject:
			resp.Diagnostics.Append(planmodifiers.FillDefault(ctx, req.Config, &resp.Plan, f.attr, "MTNCLOUD_PROJECT", d.Project)...)
		}
	}
	if resp.Diagnostics.HasError() {
		return
	}
	if !req.State.Raw.IsNull() {
		// After the order, nothing can change it. Only an import leaves every input null in state: that
		// order adopts its configuration.
		adopting := true
		for name := range r.inputs() {
			var before attr.Value
			resp.Diagnostics.Append(req.State.GetAttribute(ctx, path.Root(name), &before)...)
			if before != nil && !before.IsNull() {
				if l, ok := before.(types.List); !ok || len(l.Elements()) > 0 {
					adopting = false
				}
			}
		}
		for name := range r.inputs() {
			var before, after attr.Value
			resp.Diagnostics.Append(req.State.GetAttribute(ctx, path.Root(name), &before)...)
			resp.Diagnostics.Append(resp.Plan.GetAttribute(ctx, path.Root(name), &after)...)
			if adopting || after == nil || after.IsUnknown() || (before != nil && before.Equal(after)) {
				continue
			}
			resp.Diagnostics.AddAttributeError(path.Root(name), "Can't change an ordered "+r.spec.item,
				fmt.Sprintf("%s was ordered with %s = %s. An order can't be changed: change it in the console, or set it "+
					"back. To order a new one, add a new resource.", r.spec.item, name, shown(before)))
		}
		if adopting && !r.spec.target {
			// The adopting update looks for the instances by name.
			var ids []string
			resp.Diagnostics.Append(req.State.GetAttribute(ctx, path.Root("instance_ids"), &ids)...)
			if len(ids) == 0 {
				resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("instance_ids"), types.ListUnknown(types.StringType))...)
			}
		}
		return
	}
	if !planKnown(ctx, resp.Plan, r.inputs()) {
		return // checked at apply
	}
	r.build(ctx, resp.Plan, req.Config, &resp.Diagnostics) // resolves every name; errors point at the attribute
}

func shown(v attr.Value) string {
	if v == nil || v.IsNull() {
		return "nothing (left out)"
	}
	return v.String()
}

func planKnown(ctx context.Context, p source, inputs map[string]field) bool {
	for name := range inputs {
		var v attr.Value
		if p.GetAttribute(ctx, path.Root(name), &v).HasError() || (v != nil && v.IsUnknown()) {
			return false
		}
	}
	return true
}

type volume struct {
	Size      types.Int64  `tfsdk:"size"`
	Datastore types.String `tfsdk:"datastore"`
}

// build resolves every input to what the order form takes. Secrets come from cfg: they are never
// in a plan.
func (r *orderResource) build(ctx context.Context, p, cfg source, diags *diag.Diagnostics) map[string]any {
	form, err := r.client.CatalogForm(ctx, r.spec.item)
	if err != nil {
		diags.AddError("Unable to read the "+r.spec.item+" order form", err.Error())
		return nil
	}
	str := func(src source, a string) string {
		var v types.String
		diags.Append(src.GetAttribute(ctx, path.Root(a), &v)...)
		return v.ValueString()
	}
	strs := func(a string) []string {
		var v []string
		diags.Append(p.GetAttribute(ctx, path.Root(a), &v)...)
		return v
	}
	fail := func(a string, err error) {
		diags.AddAttributeError(path.Root(a), "Invalid "+strings.ReplaceAll(strings.TrimSuffix(a, "_"), "_", " "), err.Error())
	}
	out := map[string]any{}
	var place *client.Placement
	var projectID int64
	for _, f := range r.spec.fields {
		if f.kind == kGroup {
			pl, err := r.client.PlacementForGroup(ctx, str(p, f.attr))
			if err != nil {
				fail(f.attr, err)
				return nil
			}
			place = pl
			out[f.key] = strconv.FormatInt(pl.GroupID, 10)
			if _, ok := form.Fields["cloud"]; ok {
				out["cloud"] = strconv.FormatInt(pl.CloudID, 10)
			}
		}
	}
	for _, f := range r.spec.fields {
		if f.kind == kProject && place != nil {
			if name := str(p, f.attr); name != "" {
				pr, err := r.client.ProjectByNameAt(ctx, place, name)
				if err != nil {
					fail(f.attr, err)
					return nil
				}
				projectID = pr.ID
				out[f.key] = "pool-" + strconv.FormatInt(pr.ID, 10)
			}
		}
	}
	for _, f := range r.spec.fields {
		switch f.kind {
		case kGroup, kProject:
		case kText, kSelect:
			v := str(p, f.attr)
			if v == "" {
				v = f.def
			}
			if v == "" {
				continue
			}
			if f.kind == kSelect {
				ff, ok := form.Fields[f.key]
				if !ok {
					fail(f.attr, fmt.Errorf("the %s order form has no %q field any more; leave %s out, or update the provider", r.spec.item, f.key, f.attr))
					continue
				}
				c, err := r.client.FormChoice(ctx, ff, strings.ReplaceAll(f.attr, "_", " "), v)
				if err != nil {
					fail(f.attr, err)
					continue
				}
				v = c.Value
			}
			out[f.key] = v
		case kSecret:
			if v := str(cfg, f.attr); v != "" {
				out[f.key] = v
			}
		case kNumber, kBytes:
			var v types.Int64
			diags.Append(p.GetAttribute(ctx, path.Root(f.attr), &v)...)
			n, set := v.ValueInt64(), !v.IsNull()
			if !set && f.def != "" {
				n, _ = strconv.ParseInt(f.def, 10, 64)
				set = true
			}
			if set {
				if f.kind == kBytes {
					n *= 1 << 30
				}
				out[f.key] = n
			}
		case kFlag:
			var v types.Bool
			diags.Append(p.GetAttribute(ctx, path.Root(f.attr), &v)...)
			switch {
			case !v.IsNull():
				out[f.key] = v.ValueBool()
			case f.def != "":
				out[f.key] = f.def == "true"
			}
		case kList, kLabels:
			if v := strs(f.attr); len(v) > 0 {
				out[f.key] = strings.Join(v, ",")
			}
		case kLines:
			v := strs(f.attr)
			if len(v) == 0 && f.def != "" {
				v = []string{f.def}
			}
			if len(v) > 0 {
				out[f.key] = strings.Join(v, "\n")
			}
		case kEnvironment:
			if v := str(p, f.attr); v != "" {
				env, err := r.client.EnvironmentByName(ctx, v)
				if err != nil {
					fail(f.attr, err)
					continue
				}
				out[f.key] = env.Value
			}
		case kFloatingIP:
			if v := str(p, f.attr); v != "" {
				ff, ok := form.Fields[f.key]
				if !ok {
					fail(f.attr, fmt.Errorf("the %s order form has no floating IP field", r.spec.item))
					continue
				}
				c, err := r.client.FormChoice(ctx, ff, "floating IP pool", v)
				if err != nil {
					fail(f.attr, err)
					continue
				}
				out[f.key] = c.Value
			}
		case kPlan:
			v := str(p, f.attr)
			if v == "" || place == nil || projectID == 0 {
				continue
			}
			pl, err := r.client.PlanByName(ctx, place, projectID, form.LayoutID(f.key, "plan", "resourcepool", "networks"), v)
			if err != nil {
				fail(f.attr, err)
				continue
			}
			out[f.key] = pl.ID
		case kNetworks:
			var nets []any
			for i, name := range strs(f.attr) {
				n, err := r.client.FindNetwork(ctx, name, projectID)
				if err == nil && n == nil {
					err = fmt.Errorf("no network named %q in the project", name)
				}
				if err != nil {
					fail(f.attr, err)
					continue
				}
				nets = append(nets, map[string]any{"primaryInterface": i == 0, "network": map[string]any{"id": "network-" + strconv.FormatInt(n.ID, 10)}})
			}
			if len(nets) > 0 {
				out[f.key] = nets
			}
		case kSecGroups:
			var sgs []any
			for _, name := range strs(f.attr) {
				sg, err := r.client.SecurityGroupByName(ctx, place, projectID, name)
				if err != nil {
					fail(f.attr, err)
					continue
				}
				sgs = append(sgs, map[string]any{"id": sg.ID})
			}
			if len(sgs) > 0 {
				out[f.key] = sgs
			}
		case kVolumes:
			var root types.Int64
			diags.Append(p.GetAttribute(ctx, path.Root(f.attr+"root_volume_size"), &root)...)
			var data []volume
			diags.Append(p.GetAttribute(ctx, path.Root(f.attr+"data_volume"), &data)...)
			if root.IsNull() {
				if len(data) > 0 || str(p, f.attr+"datastore") != "" {
					diags.AddAttributeError(path.Root(f.attr+"root_volume_size"), "Root disk size missing",
						"Set "+f.attr+"root_volume_size too: extra disks and a datastore are ordered with the root disk.")
				}
				continue
			}
			if place == nil {
				continue
			}
			all := append([]volume{{Size: root, Datastore: types.StringValue(str(p, f.attr+"datastore"))}}, data...)
			var vols []any
			for i, v := range all {
				vol := map[string]any{"name": "root", "rootVolume": i == 0, "size": v.Size.ValueInt64()}
				if i > 0 {
					vol["name"] = "data-" + strconv.Itoa(i)
				}
				if ds := v.Datastore.ValueString(); ds != "" {
					o, err := r.client.DatastoreByName(ctx, place, projectID, ds)
					if err != nil {
						fail(f.attr+"datastore", err)
						continue
					}
					vol["storageType"] = o.ID
				}
				vols = append(vols, vol)
			}
			out[f.key] = vols
		}
	}
	return out
}

func (r *orderResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	if !r.ready(&resp.Diagnostics) {
		return
	}
	var to timeouts.Value
	resp.Diagnostics.Append(req.Plan.GetAttribute(ctx, path.Root("timeouts"), &to)...)
	timeout, d := to.Create(ctx, orderTimeout)
	resp.Diagnostics.Append(d...)
	config := r.build(ctx, req.Plan, req.Config, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var item *client.OrderedItem
	var err error
	if r.spec.target {
		var target types.String
		resp.Diagnostics.Append(req.Plan.GetAttribute(ctx, path.Root("instance_id"), &target)...)
		id, perr := strconv.ParseInt(target.ValueString(), 10, 64)
		if perr != nil {
			resp.Diagnostics.AddAttributeError(path.Root("instance_id"), "Invalid instance ID", fmt.Sprintf("%q is not an instance ID.", target.ValueString()))
			return
		}
		item, err = r.client.OrderForInstance(ctx, r.spec.item, id, config)
	} else {
		item, err = r.client.OrderWorkflow(ctx, r.spec.item, config)
	}
	if err != nil {
		msg := err.Error()
		if !client.IsClientError(err) {
			msg += ". The order may have been placed: check Catalog > Inventory before applying again, and import it " +
				"(terraform import <address> <order id>) if it is there."
		}
		resp.Diagnostics.AddError("Unable to order "+r.spec.item, msg)
		return
	}
	// Saved first: whatever happens next, a retry never orders a second one.
	resp.State.Raw = req.Plan.Raw
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), strconv.FormatInt(item.ID, 10))...)
	got, err := r.client.WaitForExecution(ctx, item.ID)
	if got != nil {
		item = got
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("status"), executionStatus(item))...)
	if !r.spec.target {
		resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("instance_ids"), types.ListValueMust(types.StringType, nil))...)
	}
	done, ok := client.ExecutionDone(item.Execution)
	switch {
	case done && !ok, strings.EqualFold(item.Status, "failed"):
		resp.Diagnostics.AddError(r.spec.item+" order failed",
			fmt.Sprintf("Order %d ended with %q%s. Check it under Catalog > Inventory, and delete anything it left in the "+
				"console before applying again (the next apply orders again).", item.ID, executionStatus(item), message(item)))
		resp.State.RemoveResource(ctx)
		return
	case err != nil:
		detail := err.Error()
		if errors.Is(err, context.DeadlineExceeded) {
			detail = "it hasn't reported a result yet"
		}
		resp.Diagnostics.AddWarning(r.spec.item+" ordered, result not known yet",
			fmt.Sprintf("Order %d was placed, but its result isn't known: %s. The next refresh reads it again.", item.ID, detail))
		return
	}
	if r.spec.target {
		return
	}
	ids := r.findInstances(ctx, req.Plan, &resp.Diagnostics)
	for _, id := range ids {
		if _, err := r.client.WaitForInstance(ctx, id); err != nil {
			resp.Diagnostics.AddWarning("Instance not running yet",
				fmt.Sprintf("Instance %d from order %d isn't running yet: %s. The next refresh reads it again.", id, item.ID, err))
		}
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("instance_ids"), idList(ids))...)
}

// findInstances finds the instances an order made: named after it (or "<name>-<n>") in its project.
func (r *orderResource) findInstances(ctx context.Context, p source, diags *diag.Diagnostics) []int64 {
	var name, project types.String
	diags.Append(p.GetAttribute(ctx, path.Root("name"), &name)...)
	diags.Append(p.GetAttribute(ctx, path.Root("project"), &project)...)
	all, err := r.client.ListInstances(ctx)
	if err != nil {
		diags.AddWarning("Unable to list instances", err.Error())
		return nil
	}
	pr, err := r.client.ProjectByName(ctx, project.ValueString())
	if err != nil {
		diags.AddWarning("Unable to find the order's instances", "The project couldn't be read: "+err.Error()+". The next refresh looks again.")
		return nil
	}
	var ids []int64
	for _, in := range all {
		if in.ProjectID == pr.ID && madeBy(in.Name, name.ValueString()) {
			ids = append(ids, in.ID)
		}
	}
	if len(ids) == 0 {
		diags.AddWarning("No instance found for "+r.spec.item,
			fmt.Sprintf("The order succeeded, but no instance named %q is in the project yet. The next refresh looks again.", name.ValueString()))
	}
	return ids
}

// madeBy reports whether an instance is named after an order: "<name>", or "<name>-<n>" for the
// nodes of a multi-node service. Case is ignored.
func madeBy(instance, order string) bool {
	got, n := strings.ToLower(instance), strings.ToLower(order)
	if got == n {
		return true
	}
	rest, ok := strings.CutPrefix(got, n+"-")
	if !ok || rest == "" {
		return false
	}
	for _, c := range rest {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

func idList(ids []int64) types.List {
	vals := make([]attr.Value, 0, len(ids))
	for _, id := range ids {
		vals = append(vals, types.StringValue(strconv.FormatInt(id, 10)))
	}
	return types.ListValueMust(types.StringType, vals)
}

func (r *orderResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	if !r.ready(&resp.Diagnostics) {
		return
	}
	var idv, status types.String
	resp.Diagnostics.Append(req.State.GetAttribute(ctx, path.Root("id"), &idv)...)
	resp.Diagnostics.Append(req.State.GetAttribute(ctx, path.Root("status"), &status)...)
	id, err := strconv.ParseInt(idv.ValueString(), 10, 64)
	if err != nil {
		resp.Diagnostics.AddError("Invalid order ID", fmt.Sprintf("%q is not a catalog order ID.", idv.ValueString()))
		return
	}
	item, err := r.client.GetOrderedItem(ctx, id)
	if client.IsNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Unable to read the order", err.Error())
		return
	}
	if item.TypeName != r.spec.item {
		resp.Diagnostics.AddError("Wrong catalog item",
			fmt.Sprintf("Order %d is a %q, not a %q. Import it with the resource for its item.", id, item.TypeName, r.spec.item))
		return
	}
	if done, ok := client.ExecutionDone(item.Execution); (done && !ok) || strings.EqualFold(item.Status, "failed") {
		resp.Diagnostics.AddWarning(r.spec.item+" order failed",
			fmt.Sprintf("Order %d ended with %q%s. The next apply orders it again.", id, executionStatus(item), message(item)))
		resp.State.RemoveResource(ctx)
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("status"), executionStatus(item))...)
	if r.spec.target {
		return
	}
	var ids []string
	resp.Diagnostics.Append(req.State.GetAttribute(ctx, path.Root("instance_ids"), &ids)...)
	var found []int64
	if len(ids) == 0 {
		var name types.String
		resp.Diagnostics.Append(req.State.GetAttribute(ctx, path.Root("name"), &name)...)
		if !name.IsNull() {
			found = r.findInstances(ctx, req.State, &resp.Diagnostics)
		}
	} else {
		for _, s := range ids {
			n, _ := strconv.ParseInt(s, 10, 64)
			_, err := r.client.GetInstance(ctx, n)
			switch {
			case client.IsNotFound(err):
			case err != nil:
				resp.Diagnostics.AddError("Unable to read instance "+s, err.Error())
				return
			default:
				found = append(found, n)
			}
		}
		if len(found) == 0 {
			// Everything it made was deleted: plan a new order (rule 8).
			resp.State.RemoveResource(ctx)
			return
		}
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("instance_ids"), idList(found))...)
}

// Update only runs when an imported order adopts its configuration (ModifyPlan refuses any other
// change): nothing is sent. The inventory doesn't show an order's name, so the instances it made
// are found now, by the configuration's name.
func (r *orderResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	resp.State.Raw = req.Plan.Raw
	for _, a := range []string{"id", "status"} {
		var v attr.Value
		resp.Diagnostics.Append(req.State.GetAttribute(ctx, path.Root(a), &v)...)
		resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root(a), v)...)
	}
	if r.spec.target {
		return
	}
	var ids []string
	resp.Diagnostics.Append(req.State.GetAttribute(ctx, path.Root("instance_ids"), &ids)...)
	found := make([]int64, 0, len(ids))
	for _, s := range ids {
		n, _ := strconv.ParseInt(s, 10, 64)
		found = append(found, n)
	}
	if len(found) == 0 && r.ready(&resp.Diagnostics) {
		found = r.findInstances(ctx, req.Plan, &resp.Diagnostics)
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("instance_ids"), idList(found))...)
}

// Delete refuses: an order can't be undone from Terraform (owner, D-028).
func (r *orderResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var id types.String
	resp.Diagnostics.Append(req.State.GetAttribute(ctx, path.Root("id"), &id)...)
	where := "under Instances"
	switch {
	case r.spec.managed:
		where = "under Apps"
	case r.spec.target:
		where = "on the instance (the certificate stays until the instance is deleted)"
	}
	resp.Diagnostics.AddError(r.spec.item+" can't be deleted from Terraform",
		fmt.Sprintf("Order %s made something MTN Cloud deletes from the console: delete it %s. To stop managing it, "+
			"use a removed block (lifecycle { destroy = false }) or run terraform state rm.", id.ValueString(), where))
}

func (r *orderResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	if _, err := strconv.ParseInt(req.ID, 10, 64); err != nil {
		resp.Diagnostics.AddError("Invalid import ID",
			fmt.Sprintf("Import a %s by its catalog order ID (Catalog > Inventory), for example 9801. Got %q.", r.spec.item, req.ID))
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
}
