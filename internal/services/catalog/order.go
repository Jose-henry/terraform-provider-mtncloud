package catalog

import (
	"context"
	"errors"
	"fmt"
	"slices"
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
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/mapplanmodifier"
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
	_ resource.ResourceWithMoveState   = (*orderResource)(nil)
)

// orderResource is one catalog item, ordered (D-026). Its inputs can't change after the order, so a
// changed input replaces it (owner, 2026-10-30). Destroy deletes what the order made, its app or its instances, then its
// inventory record (D-029); an order that makes nothing deletable is only removed from state.
type orderResource struct {
	base
	spec orderSpec
}

func orderResources() []func() resource.Resource {
	out := []func() resource.Resource{}
	for _, s := range orderSpecs() {
		out = append(out, func() resource.Resource { return &orderResource{spec: s} })
		if s.movedFrom != "" {
			alias := s
			alias.typeName, alias.movedFrom, alias.deprecated = s.movedFrom, "", s.typeName
			if s.oldFields != nil {
				alias.fields = s.oldFields
			}
			out = append(out, func() resource.Resource { return &orderResource{spec: alias} })
		}
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
	if !r.spec.target {
		out["extra_inputs"] = field{attr: "extra_inputs", kind: kExtra}
	}
	return out
}

func (r *orderResource) Schema(ctx context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	where := "Instances"
	if r.spec.app {
		where = "Apps"
	}
	desc := r.spec.about + " Ordered from MTN Cloud's catalog (" + r.spec.item + "). An order can't be changed once " +
		"placed: changing any input replaces it (what the old order made is deleted, then a new one is ordered), and " +
		"`terraform apply -replace=<address>` orders it again. Add `lifecycle { prevent_destroy = true }` to stop that. " +
		"The apply succeeds only when the order's workflow succeeded and every VM it made is running. Inputs are checked " +
		"at plan time against the item's live order form: what the form requires, its choices and its stated minimums. " +
		"`terraform destroy` deletes what it made, shown under " + where + " in the console, with its disks and floating " +
		"IPs, then the order's record in Catalog > Inventory. Secrets are write-only (Terraform 1.11 or newer): they are " +
		"sent with the order and never stored."
	if r.spec.target {
		desc = r.spec.about + " Ordered from MTN Cloud's catalog (" + r.spec.item + "). Changing an input orders it " +
			"again. `terraform destroy` deletes nothing: the certificate stays on the instance, and the resource is " +
			"only removed from state, with a warning."
	}
	keep := []planmodifier.String{stringplanmodifier.UseStateForUnknown()}
	attrs := map[string]schema.Attribute{
		"id":     schema.StringAttribute{Computed: true, Description: "Order ID (Catalog > Inventory).", PlanModifiers: keep},
		"status": schema.StringAttribute{Computed: true, Description: "The order's workflow result, for example `success`.", PlanModifiers: keep},
	}
	blocks := map[string]schema.Block{"timeouts": timeouts.Block(ctx, timeouts.Opts{Create: true, Delete: true})}
	if r.spec.target {
		attrs["instance_id"] = schema.StringAttribute{Required: true,
			Description: "ID of the instance to install the certificate on, for example `mtncloud_instance.web.id`."}
	} else {
		attrs["instance_ids"] = schema.ListAttribute{Computed: true, ElementType: types.StringType,
			Description:   "IDs of the instances the order created, found by name in the project.",
			PlanModifiers: []planmodifier.List{listplanmodifier.UseStateForUnknown()}}
		attrs["instance_statuses"] = schema.MapAttribute{Computed: true, ElementType: types.StringType,
			Description: "Status of each instance the order created, by instance ID, for example `running`. A `failed` " +
				"instance plans a replace: the order is deleted and placed again.",
			PlanModifiers: []planmodifier.Map{mapplanmodifier.UseStateForUnknown()}}
		// Sensitive: a form field can be a password, and these are sent as they are.
		attrs["extra_inputs"] = schema.MapAttribute{Optional: true, Sensitive: true, ElementType: types.StringType,
			Description: "Other inputs of the item's order form, by the form's field name, for fields this resource has " +
				"no argument for yet (MTN adds and changes form fields). A choice is given by its shown name or value. " +
				"`data.mtncloud_catalog_item` lists the form's fields. Sensitive, as a field can be a password; the " +
				"values are kept in state. Changing them orders again."}
	}
	for _, f := range r.spec.fields {
		switch f.kind {
		case kVolumes:
			attrs[f.attr+"root_volume_size"] = schema.Int64Attribute{Optional: !f.required, Required: f.required,
				Description: r.rootDiskDescription(), Validators: []validator.Int64{int64validator.AtLeast(1)}}
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
	desc += r.formSummary()
	resp.Schema = schema.Schema{Description: desc, Attributes: attrs, Blocks: blocks}
	if r.spec.deprecated != "" {
		resp.Schema.DeprecationMessage = "Renamed to mtncloud_" + r.spec.deprecated + ". Rename the resource in your " +
			"configuration and add a `moved {}` block from this address to the new one (Terraform 1.8 or newer): nothing is " +
			"replaced. On older Terraform, run `terraform state rm` on this address, then `terraform import` the new one with the same order ID."
		resp.Schema.Description = "**Deprecated:** renamed to `mtncloud_" + r.spec.deprecated + "`. " + desc
	}
}

// MoveState accepts the state of the type's former name (`moved {}` from the deprecated alias). Every
// attribute both schemas have is carried over; one the new type dropped (PfSense's disks, now fixed
// by the form) is left behind.
func (r *orderResource) MoveState(ctx context.Context) []resource.StateMover {
	if r.spec.movedFrom == "" {
		return nil
	}
	old := r.spec
	if old.oldFields != nil {
		old.fields = old.oldFields
	}
	var src, dst resource.SchemaResponse
	(&orderResource{spec: old}).Schema(ctx, resource.SchemaRequest{}, &src)
	r.Schema(ctx, resource.SchemaRequest{}, &dst)
	return []resource.StateMover{{
		SourceSchema: &src.Schema,
		StateMover: func(ctx context.Context, req resource.MoveStateRequest, resp *resource.MoveStateResponse) {
			if req.SourceTypeName != "mtncloud_"+r.spec.movedFrom || !strings.HasSuffix(req.SourceProviderAddress, "/mtncloud") {
				return
			}
			if req.SourceState == nil {
				resp.Diagnostics.AddError("Unable to move "+req.SourceTypeName,
					"The saved state doesn't match the resource's schema. Run `terraform apply` with the previous name first, then move it.")
				return
			}
			if r.spec.oldFields == nil {
				resp.TargetState.Raw = req.SourceState.Raw
				return
			}
			for name := range dst.Schema.Attributes {
				if _, ok := src.Schema.Attributes[name]; !ok {
					continue
				}
				var v attr.Value
				resp.Diagnostics.Append(req.SourceState.GetAttribute(ctx, path.Root(name), &v)...)
				resp.Diagnostics.Append(resp.TargetState.SetAttribute(ctx, path.Root(name), v)...)
			}
		},
	}}
}

func (r *orderResource) attribute(f field) schema.Attribute {
	desc := f.desc
	if f.attr == "name" && r.spec.app {
		desc = "Name of the app the order creates, shown under Apps. It must be unique among your apps: the provider " +
			"finds the app, and the VMs in it, by this name (MTN names the VMs itself)."
	}
	d := desc + snapshotFor(r.spec.typeName).field(f.key).facts()
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

// ModifyPlan fills the provider's group and project, plans a replace for a changed input or a failed
// instance, and checks every input against the item's live form before anything is ordered.
func (r *orderResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.Plan.Raw.IsNull() || r.client == nil {
		return // destroy, or an unconfigured provider
	}
	d := r.client.DefaultsFor(ctx)
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
		// An order can't be changed: a changed input orders it again (owner, 2026-10-30), which also
		// lets -replace and destroy plan with the new value in the configuration.
		replace := false
		for name := range r.inputs() {
			var before, after attr.Value
			resp.Diagnostics.Append(req.State.GetAttribute(ctx, path.Root(name), &before)...)
			resp.Diagnostics.Append(resp.Plan.GetAttribute(ctx, path.Root(name), &after)...)
			if adopting || after == nil {
				continue
			}
			switch inputChange(name, before, after) {
			case noChange:
				continue
			case recorded:
				resp.Diagnostics.AddAttributeWarning(path.Root(name), "Root disk size recorded",
					"This order was placed without root_volume_size. The size is recorded in state; the disk isn't changed "+
						"or checked. Set it to the size the order was placed with.")
				continue
			}
			resp.RequiresReplace.Append(path.Root(name))
			replace = true
		}
		if !r.spec.target && !replace && !adopting {
			r.replaceFailed(ctx, req, resp)
		}
		if adopting && !r.spec.target {
			// The adopting update looks for the instances by name.
			var ids []string
			resp.Diagnostics.Append(req.State.GetAttribute(ctx, path.Root("instance_ids"), &ids)...)
			if len(ids) == 0 {
				resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("instance_ids"), types.ListUnknown(types.StringType))...)
				resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("instance_statuses"), types.MapUnknown(types.StringType))...)
			}
		}
		return
	}
	if !planKnown(ctx, resp.Plan, r.inputs()) {
		// Terraform plans again during apply once every input is known, so this runs then, before Create.
		return
	}
	r.build(ctx, resp.Plan, req.Config, true, &resp.Diagnostics) // resolves every name; errors point at the attribute
}

func planKnown(ctx context.Context, p source, inputs map[string]field) bool {
	for name := range inputs {
		var v attr.Value
		if p.GetAttribute(ctx, path.Root(name), &v).HasError() {
			return false
		}
		if v == nil {
			continue
		}
		// A list or map can be known while an element isn't (a name from a resource made in the same apply).
		if tv, err := v.ToTerraformValue(ctx); err != nil || !tv.IsFullyKnown() {
			return false
		}
	}
	return true
}

// sameInput reports whether an input is unchanged. A collection left out and an empty one are the
// same order: a module passing `{}` where nothing was set must not order again.
func sameInput(before, after attr.Value) bool {
	if before == nil {
		return false
	}
	if before.Equal(after) {
		return true
	}
	return emptyOrNull(before) && emptyOrNull(after)
}

func emptyOrNull(v attr.Value) bool {
	if v.IsNull() {
		return true
	}
	switch c := v.(type) {
	case types.Map:
		return !c.IsUnknown() && len(c.Elements()) == 0
	case types.List:
		return !c.IsUnknown() && len(c.Elements()) == 0
	case types.Set:
		return !c.IsUnknown() && len(c.Elements()) == 0
	}
	return false
}

type volume struct {
	Size      types.Int64  `tfsdk:"size"`
	Datastore types.String `tfsdk:"datastore"`
}

// build resolves every input to what the order form takes. Secrets come from cfg: they are never
// in a plan.
// build resolves every name in the plan into the order's form values. checkPools is true at plan
// time only: the floating IP pool check reads routers, and its warning should show once, not again
// at create.
func (r *orderResource) build(ctx context.Context, p, cfg source, checkPools bool, diags *diag.Diagnostics) map[string]any {
	form, err := r.client.CatalogForm(ctx, r.spec.ref())
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
	// A pool attribute pairs with the networks attribute of the same prefix: floating_ip_pool with
	// networks, worker_floating_ip_pool with worker_networks.
	primary := map[string]int64{} // prefix -> the first network's ID
	pools := map[string]string{}  // pool attribute -> the external network's name
	fail := func(a string, err error) {
		diags.AddAttributeError(path.Root(a), "Invalid "+strings.ReplaceAll(strings.TrimSuffix(a, "_"), "_", " "), err.Error())
	}
	out := map[string]any{}
	planNames := map[string]string{} // plan attribute -> the chosen plan's name on the form
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
				// Comma-separated, as the form's help and pattern ask ("10.60.50.0/24, 10.60.40.0/24"):
				// one per line failed its pattern (review, 2026-10-31).
				out[f.key] = strings.Join(v, ", ")
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
				pools[f.attr] = c.Name // the external network's name, even when the value ("pool-<id>") was written
			}
		case kPlan:
			v := str(p, f.attr)
			if v == "" || place == nil || projectID == 0 {
				continue
			}
			// A form's plan list (option list "Service Plans") is its own: its IDs differ from the
			// instance wizard's (G2S4 is 13107 there, 1969 here, B-019), and some forms name their
			// plans their own way ("Small (G2S4)"). Only the form is asked, with its project, cloud
			// and group, as the console does (owner, 2026-10-30).
			ff, ok := form.Fields[f.key]
			if !ok || ff.Type != "select" {
				fail(f.attr, fmt.Errorf("the %s order form has no plan list %q any more; update the provider", r.spec.item, f.key))
				continue
			}
			c, err := r.client.FormChoiceIn(ctx, ff, formValues(form, out), "plan", v)
			if err != nil {
				fail(f.attr, err)
				continue
			}
			n, err := strconv.ParseInt(c.Value, 10, 64)
			if err != nil {
				fail(f.attr, fmt.Errorf("plan %q has the value %q, not an ID", v, c.Value))
				continue
			}
			out[f.key] = n
			planNames[f.attr] = c.Name
		case kNetworks:
			var nets []any
			for i, name := range strs(f.attr) {
				n, err := r.findNetwork(ctx, name, projectID)
				if err == nil && n == nil {
					err = fmt.Errorf("no network named %q, or with that ID, in the project. For a network made in the same "+
						"apply, give its ID (mtncloud_network.<name>.id): its name is known before the network exists", name)
				}
				if err != nil {
					fail(f.attr, err)
					continue
				}
				if i == 0 {
					primary[strings.TrimSuffix(f.attr, "networks")] = n.ID
				}
				nets = append(nets, map[string]any{"primaryInterface": i == 0, "network": map[string]any{"id": "network-" + strconv.FormatInt(n.ID, 10)}})
			}
			if len(nets) > 0 {
				out[f.key] = nets
			}
		case kSecGroups:
			ids, err := r.client.SecurityGroupIDs(ctx, place, projectID, strs(f.attr))
			if err != nil {
				fail(f.attr, err)
				continue
			}
			if len(ids) > 0 {
				sgs := make([]any, len(ids))
				for i, id := range ids {
					sgs[i] = map[string]any{"id": id}
				}
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
	if diags.HasError() {
		return nil
	}
	r.applyForm(ctx, form, p, out, planNames, checkPools, diags)
	// The floating IP is attached through the primary network's router, so check the pool now
	// rather than after the order has built its VMs.
	for attr, pool := range pools {
		net := poolNetwork(attr, primary)
		if !checkPools || net == 0 {
			continue
		}
		switch msg, isErr := r.client.PoolRouterProblem(ctx, net, pool); {
		case msg == "":
		case isErr:
			diags.AddAttributeError(path.Root(attr), "Floating IP pool doesn't match the router", msg)
		default:
			diags.AddAttributeWarning(path.Root(attr), "Floating IP pool not checked against a router", msg)
		}
	}
	return out
}

// poolNetwork is the primary network a floating IP pool attribute is attached through: the first
// network of the attribute with the same prefix (worker_floating_ip_pool: worker_networks), or 0.
func poolNetwork(attr string, primary map[string]int64) int64 {
	return primary[strings.TrimSuffix(attr, "floating_ip_pool")]
}

func (r *orderResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	if !r.ready(&resp.Diagnostics) {
		return
	}
	var to timeouts.Value
	resp.Diagnostics.Append(req.Plan.GetAttribute(ctx, path.Root("timeouts"), &to)...)
	timeout, d := to.Create(ctx, orderTimeout)
	resp.Diagnostics.Append(d...)
	config := r.build(ctx, req.Plan, req.Config, false, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	itemName, err := r.itemName(ctx, r.spec.ref())
	if err != nil {
		resp.Diagnostics.AddError("Unable to find "+r.spec.item+" in the catalog", err.Error())
		return
	}
	var item *client.OrderedItem
	var before map[int64]bool // apps with the order's name before it was placed
	if r.spec.target {
		var target types.String
		resp.Diagnostics.Append(req.Plan.GetAttribute(ctx, path.Root("instance_id"), &target)...)
		id, perr := strconv.ParseInt(target.ValueString(), 10, 64)
		if perr != nil {
			resp.Diagnostics.AddAttributeError(path.Root("instance_id"), "Invalid instance ID", fmt.Sprintf("%q is not an instance ID.", target.ValueString()))
			return
		}
		item, err = r.client.OrderForInstance(ctx, itemName, id, config)
	} else {
		if r.spec.app {
			var ok bool
			if before, ok = r.appsBefore(ctx, req.Plan, &resp.Diagnostics); !ok {
				return
			}
		}
		item, err = r.client.OrderWorkflow(ctx, itemName, config)
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
		resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("instance_statuses"), types.MapValueMust(types.StringType, nil))...)
	}
	done, ok := client.ExecutionDone(item.Execution)
	switch {
	case done && !ok, strings.EqualFold(item.Status, "failed"):
		why := message(item)
		if item.Execution != nil {
			if f := r.client.ExecutionFailure(ctx, item.Execution.ID, r.secretValues(ctx, req.Plan, config)); f != "" {
				why += ". The workflow says: " + strings.TrimRight(f, ". ")
			}
		}
		resp.Diagnostics.AddError(r.spec.item+" order failed",
			fmt.Sprintf("Order %d ended with %q%s. Check it under Catalog > Inventory, and delete anything it left in the "+
				"console before applying again (the next apply orders again).", item.ID, executionStatus(item), why))
		resp.State.RemoveResource(ctx)
		return
	case err != nil:
		// Success is the workflow's result and what it made, both finished (owner, 2026-10-30): an
		// order that hasn't reported one fails the apply, and the resource is replaced next time.
		detail := err.Error()
		if errors.Is(err, context.DeadlineExceeded) {
			detail = "it hasn't reported a result before the create timeout"
		}
		resp.Diagnostics.AddError(r.spec.item+" order not finished",
			fmt.Sprintf("Order %d was placed, but its workflow hasn't finished: %s. The next apply deletes what it made "+
				"and orders it again; raise timeouts.create if the workflow is slow.", item.ID, detail))
		return
	}
	if r.spec.target {
		return
	}
	if r.spec.app && !r.appReady(ctx, req.Plan, item.ID, before, &resp.Diagnostics) {
		return
	}
	ids := r.findInstances(ctx, req.Plan, &resp.Diagnostics)
	statuses := map[string]string{}
	for _, id := range ids {
		in, err := r.client.WaitForInstance(ctx, id)
		key := strconv.FormatInt(id, 10)
		switch {
		case err != nil:
			statuses[key] = "provisioning"
			resp.Diagnostics.AddError(r.spec.item+" instance not ready",
				fmt.Sprintf("Instance %d from order %d didn't finish provisioning: %s. The next apply deletes the order with "+
					"what it made and orders it again; raise timeouts.create if it is only slow.", id, item.ID, err))
		case client.InstanceFailed(in.Status):
			statuses[key] = in.Status
			why := in.StatusMessage
			if why == "" {
				why = "no reason given"
			}
			resp.Diagnostics.AddError(r.spec.item+" instance failed",
				fmt.Sprintf("Instance %d (%s) from order %d is %q: %s. The order's workflow succeeded, but what it made "+
					"didn't. The next apply deletes the order with what it made and orders it again.", id, in.Name, item.ID, in.Status, why))
		default:
			statuses[key] = in.Status
		}
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("instance_ids"), idList(ids))...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("instance_statuses"), statusMap(statuses))...)
}

func statusMap(m map[string]string) types.Map {
	vals := make(map[string]attr.Value, len(m))
	for k, v := range m {
		vals[k] = types.StringValue(v)
	}
	return types.MapValueMust(types.StringType, vals)
}

// findInstances finds the instances an order made, by name in its project (instancesNamed).
func (r *orderResource) findInstances(ctx context.Context, p source, diags *diag.Diagnostics) []int64 {
	var name types.String
	diags.Append(p.GetAttribute(ctx, path.Root("name"), &name)...)
	ids, err := r.instancesNamed(ctx, p, diags)
	if err != nil {
		diags.AddWarning("Unable to find the order's instances", err.Error()+". The next refresh looks again.")
		return nil
	}
	if len(ids) == 0 {
		diags.AddWarning("No instance found for "+r.spec.item,
			fmt.Sprintf("The order succeeded, but no instance named %q is in the project yet. The next refresh looks again.", name.ValueString()))
	}
	return ids
}

// instancesNamed lists the instances in the order's project named after it (madeBy).
func (r *orderResource) instancesNamed(ctx context.Context, p source, diags *diag.Diagnostics) ([]int64, error) {
	var name, project types.String
	diags.Append(p.GetAttribute(ctx, path.Root("name"), &name)...)
	diags.Append(p.GetAttribute(ctx, path.Root("project"), &project)...)
	all, err := r.client.ListInstances(ctx)
	if err != nil {
		return nil, fmt.Errorf("the instances couldn't be listed: %w", err)
	}
	pr, err := r.client.ProjectByName(ctx, project.ValueString())
	if err != nil {
		return nil, fmt.Errorf("the project couldn't be read: %w", err)
	}
	var ids []int64
	inProject := map[int64]bool{}
	for _, in := range all {
		inProject[in.ID] = in.ProjectID == pr.ID
		if in.ProjectID == pr.ID && madeBy(in.Name, name.ValueString(), r.spec.app) {
			ids = append(ids, in.ID)
		}
	}
	if r.spec.app {
		// The app's VMs are named from MTN's template ("<initials>-<account>-MTN-CLOUD-MYSQL-1"),
		// so they are those of the app named after the order, in its project: app names repeat
		// across projects (review, 2026-10-31).
		apps, err := r.client.ListApps(ctx)
		if err != nil {
			return nil, fmt.Errorf("the apps couldn't be listed: %w", err)
		}
		for _, a := range client.AppsNamed(apps, name.ValueString()) {
			for _, id := range a.InstanceIDs {
				if inProject[id] && !slices.Contains(ids, id) {
					ids = append(ids, id)
				}
			}
		}
	}
	return ids, nil
}

// appsBefore notes the apps with the order's name before it is placed, so the wait never takes
// one of them for the new app. One already in the order's project stops the order: its VMs would
// be taken for this order's, and deleted with it.
func (r *orderResource) appsBefore(ctx context.Context, p source, diags *diag.Diagnostics) (map[int64]bool, bool) {
	var name types.String
	diags.Append(p.GetAttribute(ctx, path.Root("name"), &name)...)
	if ids, err := r.instancesNamed(ctx, p, diags); err != nil {
		diags.AddError("Unable to check the name", err.Error()+". Nothing was ordered; apply again.")
		return nil, false
	} else if len(ids) > 0 {
		diags.AddAttributeError(path.Root("name"), "Name already in use",
			fmt.Sprintf("An app or instance named %q is already in the project (instance %d). Nothing was ordered: choose "+
				"another name, or import the order that made it (terraform import <address> <order id>).", name.ValueString(), ids[0]))
		return nil, false
	}
	apps, err := r.client.ListApps(ctx)
	if err != nil {
		diags.AddError("Unable to list the apps", err.Error()+". Nothing was ordered; apply again.")
		return nil, false
	}
	before := map[int64]bool{}
	for _, a := range client.AppsNamed(apps, name.ValueString()) {
		before[a.ID] = true
	}
	return before, true
}

// secretValues are the order's secret values, masked in anything shown from its workflow:
// write-only arguments and extra_inputs (a form field there can be a password).
func (r *orderResource) secretValues(ctx context.Context, p source, config map[string]any) []string {
	var out []string
	for _, f := range r.spec.fields {
		if s, ok := config[f.key].(string); ok && f.kind == kSecret {
			out = append(out, s)
		}
	}
	var extra map[string]string
	p.GetAttribute(ctx, path.Root("extra_inputs"), &extra)
	for _, v := range extra {
		if len(v) >= 4 {
			out = append(out, v)
		}
	}
	return out
}

// appReady waits for the app an order's workflow started to finish provisioning: the workflow
// ends once it has asked for the app. False, with an error, when it didn't come up running.
func (r *orderResource) appReady(ctx context.Context, p source, orderID int64, before map[int64]bool, diags *diag.Diagnostics) bool {
	var name types.String
	diags.Append(p.GetAttribute(ctx, path.Root("name"), &name)...)
	a, err := r.client.WaitForApp(ctx, name.ValueString(), before)
	switch {
	case err != nil && a == nil && errors.Is(err, context.DeadlineExceeded):
		diags.AddError(r.spec.item+" app not found",
			fmt.Sprintf("Order %d's workflow succeeded, but no app named %q appeared before the create timeout. Check Apps "+
				"in the console; the next apply deletes the order and orders it again.", orderID, name.ValueString()))
		return false
	case err != nil:
		diags.AddError(r.spec.item+" app not ready",
			fmt.Sprintf("App %q from order %d didn't finish provisioning: %s. The next apply deletes the order with what it "+
				"made and orders it again; raise timeouts.create if it is only slow.", name.ValueString(), orderID, err))
		return false
	case !client.AppRunning(a):
		diags.AddError(r.spec.item+" app failed",
			fmt.Sprintf("App %q (%d) from order %d is %q. The order's workflow succeeded, but what it made didn't. Check it "+
				"under Apps; the next apply deletes the order with what it made and orders it again.", a.Name, a.ID, orderID, a.Status))
		return false
	}
	return true
}

// madeBy reports whether an instance is named after an order: exactly "<name>", or "<name>-<n>"
// for the nodes of a multi-node app (managed services, self-managed Kubernetes). A single-VM order never claims "<name>-2", which
// may be another order's or the user's own (destroy deletes what this matches). Case is ignored.
func madeBy(instance, order string, nodes bool) bool {
	got, n := strings.ToLower(instance), strings.ToLower(order)
	if got == n {
		return true
	}
	if !nodes {
		return false
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
	if !r.isItem(ctx, r.spec.ref(), item) {
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
	statuses := map[string]string{}
	if len(ids) == 0 {
		var name types.String
		resp.Diagnostics.Append(req.State.GetAttribute(ctx, path.Root("name"), &name)...)
		if !name.IsNull() {
			found = r.findInstances(ctx, req.State, &resp.Diagnostics)
		}
	} else {
		for _, s := range ids {
			n, _ := strconv.ParseInt(s, 10, 64)
			in, err := r.client.GetInstance(ctx, n)
			switch {
			case client.IsNotFound(err):
			case err != nil:
				resp.Diagnostics.AddError("Unable to read instance "+s, err.Error())
				return
			default:
				found = append(found, n)
				statuses[s] = in.Status
			}
		}
		if len(found) == 0 {
			// Everything it made was deleted: plan a new order (rule 8).
			resp.State.RemoveResource(ctx)
			return
		}
	}
	if len(ids) == 0 {
		statuses = r.statusesOf(ctx, found)
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("instance_ids"), idList(found))...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("instance_statuses"), statusMap(statuses))...)
}

// statusesOf reads each instance's status; one that can't be read is left out.
func (r *orderResource) statusesOf(ctx context.Context, ids []int64) map[string]string {
	out := map[string]string{}
	for _, id := range ids {
		if in, err := r.client.GetInstance(ctx, id); err == nil {
			out[strconv.FormatInt(id, 10)] = in.Status
		}
	}
	return out
}

// Update only runs when an imported order adopts its configuration (any other change is a
// replace): nothing is sent. The inventory doesn't show an order's name, so the instances it made
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
	statuses := map[string]string{}
	if r.ready(&resp.Diagnostics) {
		statuses = r.statusesOf(ctx, found)
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("instance_statuses"), statusMap(statuses))...)
}

// Delete deletes what the order made (owner, D-029), then its inventory record. An app order
// (managed services, self-managed Kubernetes): its app (named after the order) is deleted with its instances. Other instances are
// deleted through an app only when the app holds nothing else, directly otherwise; then it waits
// until they are gone. It refuses rather than guess: while the order still runs, after an import
// that hasn't been applied, when a lookup fails, or when nothing it made can be found. An order that
// makes nothing deletable (a certificate) is only removed from state.
func (r *orderResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var idv, name types.String
	resp.Diagnostics.Append(req.State.GetAttribute(ctx, path.Root("id"), &idv)...)
	if r.spec.target {
		resp.Diagnostics.AddWarning("Nothing deleted",
			fmt.Sprintf("Order %s (%s) made nothing Terraform can delete: the certificate stays on the instance until "+
				"the instance is deleted. The order is only removed from Terraform state.", idv.ValueString(), r.spec.item))
		return
	}
	if !r.ready(&resp.Diagnostics) {
		return
	}
	var to timeouts.Value
	resp.Diagnostics.Append(req.State.GetAttribute(ctx, path.Root("timeouts"), &to)...)
	resp.Diagnostics.Append(req.State.GetAttribute(ctx, path.Root("name"), &name)...)
	timeout, d := to.Delete(ctx, orderTimeout)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	orderID, _ := strconv.ParseInt(idv.ValueString(), 10, 64)
	refuse := func(why string) {
		resp.Diagnostics.AddError("Order "+idv.ValueString()+" not deleted",
			why+" Nothing was deleted. To stop managing it without deleting anything, use a removed block "+
				"(lifecycle { destroy = false }) or terraform state rm.")
	}
	if name.IsNull() {
		refuse("It was imported and not applied since, so the instances it made aren't known yet: run terraform apply first.")
		return
	}

	// An order still running would go on creating instances after they are deleted.
	item, err := r.client.GetOrderedItem(ctx, orderID)
	if err != nil && !client.IsNotFound(err) {
		refuse("The order couldn't be read: " + err.Error() + ".")
		return
	}
	// An item with no execution at all (an older order) has nothing to wait for.
	if item != nil && item.Execution != nil {
		if done, _ := client.ExecutionDone(item.Execution); !done && !strings.EqualFold(item.Status, "failed") {
			if _, err := r.client.WaitForExecution(ctx, orderID); err != nil {
				refuse("Its workflow is still running (" + err.Error() + "): destroy again once it has finished.")
				return
			}
		}
	}

	// Only instances named after the order in its project are deleted. Saved IDs narrow that
	// further, and one that no longer matches (renamed, or claimed by an older version that also
	// took "<name>-<n>" for a single-VM order) is left alone.
	named, err := r.instancesNamed(ctx, req.State, &resp.Diagnostics)
	if err != nil {
		refuse("Its instances couldn't be looked up: " + err.Error() + ".")
		return
	}
	var saved []string
	resp.Diagnostics.Append(req.State.GetAttribute(ctx, path.Root("instance_ids"), &saved)...)
	ids := named
	if len(saved) > 0 {
		ids = nil
		for _, id := range named {
			if slices.Contains(saved, strconv.FormatInt(id, 10)) {
				ids = append(ids, id)
			}
		}
	}
	apps, err := r.client.ListApps(ctx)
	if err != nil {
		refuse("Its apps couldn't be listed: " + err.Error() + ".")
		return
	}
	plan := deletePlan(apps, ids, name.ValueString(), r.spec.app)
	if len(plan.apps) == 0 && len(plan.instances) == 0 {
		refuse(fmt.Sprintf("Nothing it made was found: no instance named %q is in its project%s. Check it in the console.",
			name.ValueString(), map[bool]string{true: ", and no app has that name", false: ""}[r.spec.app]))
		return
	}
	for _, appID := range plan.apps {
		if err := r.client.DeleteApp(ctx, appID); err != nil {
			resp.Diagnostics.AddError("Unable to delete app "+strconv.FormatInt(appID, 10),
				fmt.Sprintf("Order %d's app wasn't deleted: %s. Apply again, or delete it under Apps.", orderID, err))
			return
		}
	}
	for _, id := range plan.instances {
		if err := r.client.DeleteInstance(ctx, id); err != nil {
			resp.Diagnostics.AddError("Unable to delete instance "+strconv.FormatInt(id, 10),
				fmt.Sprintf("Order %d's instance %d wasn't deleted: %s. Apply again, or delete it under Instances.", orderID, id, err))
			return
		}
	}
	for _, id := range plan.gone {
		if err := r.client.WaitForInstanceGone(ctx, id); err != nil {
			resp.Diagnostics.AddError("Instance not deleted yet",
				fmt.Sprintf("Order %d's instance %d still exists after the delete (%s). Check it in the console, or raise "+
					"`timeouts { delete = \"120m\" }`.", orderID, id, err))
			return
		}
	}
	if err := r.client.DeleteOrderedItem(ctx, orderID); err != nil {
		resp.Diagnostics.AddWarning("Order record not deleted",
			fmt.Sprintf("What order %d made is deleted, but its record in Catalog > Inventory wasn't: %s. Delete the record "+
				"in the console if you no longer want it listed.", orderID, err))
	}
}

// orderDeletion is what destroying an order deletes: apps (with their instances), instances on
// their own, and every instance to wait for.
type orderDeletion struct {
	apps, instances, gone []int64
}

// deletePlan decides how an order's instances are deleted. An app order's own app (named after
// the order, and holding at least one of its instances) goes with all its instances. Any other app goes whole only when every instance in it
// is the order's: an app that also holds someone else's instance is left, and the order's
// instances in it are deleted one by one.
func deletePlan(apps []client.App, ids []int64, name string, app bool) orderDeletion {
	var out orderDeletion
	mine := map[int64]bool{}
	for _, id := range ids {
		mine[id] = true
	}
	covered := map[int64]bool{}
	for _, a := range apps {
		holds, all := false, len(a.InstanceIDs) > 0
		for _, id := range a.InstanceIDs {
			holds = holds || mine[id]
			all = all && mine[id]
		}
		// The name alone isn't enough: another project, or the user, may have an app of that name.
		own := app && holds && strings.EqualFold(a.Name, name)
		if !own && (!holds || !all) {
			continue
		}
		out.apps = append(out.apps, a.ID)
		for _, id := range a.InstanceIDs {
			if !covered[id] {
				covered[id] = true
				out.gone = append(out.gone, id)
			}
		}
	}
	for _, id := range ids {
		if !covered[id] {
			covered[id] = true
			out.instances = append(out.instances, id)
			out.gone = append(out.gone, id)
		}
	}
	return out
}

func (r *orderResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	if _, err := strconv.ParseInt(req.ID, 10, 64); err != nil {
		resp.Diagnostics.AddError("Invalid import ID",
			fmt.Sprintf("Import a %s by its catalog order ID (Catalog > Inventory), for example 9801. Got %q.", r.spec.item, req.ID))
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
}

// findNetwork finds a project's network by name, then by ID. An ID is what a network made in the
// same apply can be given by: it stays unknown until the network exists, so it is checked then.
func (r *orderResource) findNetwork(ctx context.Context, ref string, projectID int64) (*client.Network, error) {
	n, err := r.client.FindNetwork(ctx, ref, projectID)
	if err != nil || n != nil {
		return n, err
	}
	id, ok := networkID(ref)
	if !ok {
		return nil, nil // a name, not an ID: not found
	}
	n, err = r.client.GetNetwork(ctx, id)
	if client.IsNotFound(err) || (err == nil && n.ProjectID != projectID) {
		return nil, nil
	}
	return n, err
}

func networkID(ref string) (int64, bool) {
	id, err := strconv.ParseInt(ref, 10, 64)
	return id, err == nil && id > 0
}

// rootDiskDescription states the form's own disk rules: its minimum, a data disk MTN Cloud adds,
// and the form's help text, which usually says both.
func (r *orderResource) rootDiskDescription() string {
	d := "Root disk size in GB."
	if n := r.spec.rules.minRootGB; n > 0 {
		d += fmt.Sprintf(" At least %d GB (the order form's minimum).", n)
	}
	facts := snapshotFor(r.spec.typeName).field("volumes").facts()
	if n := r.spec.rules.dataDiskGB; n > 0 && !strings.Contains(facts, "data disk") {
		d += fmt.Sprintf(" MTN Cloud also adds a required %d GB data disk itself.", n)
	}
	return d + facts
}

type change int

const (
	noChange change = iota
	recorded        // taken into state without ordering again
	reorder         // the order is placed again
)

// inputChange is what a change to one input means for an order that exists.
func inputChange(name string, before, after attr.Value) change {
	if sameInput(before, after) {
		return noChange
	}
	// root_volume_size is required since 2026-10-31: an order placed before without one records the
	// size the configuration now states, rather than ordering again.
	if name == "root_volume_size" && before != nil && before.IsNull() {
		return recorded
	}
	return reorder
}
