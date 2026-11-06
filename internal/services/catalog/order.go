package catalog

import (
	"context"
	"errors"
	"fmt"
	"net"
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
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/mtn/terraform-provider-mtncloud/internal/client"
	"github.com/mtn/terraform-provider-mtncloud/internal/planmodifiers"
)

const orderTimeout = 90 * time.Minute

// appUnchecked is the private state of an app order whose app wasn't seen to come up, because only
// the lookup failed. Read checks the app once it knows which one it is.
const appUnchecked = "app_unchecked"

var (
	_ resource.ResourceWithModifyPlan  = (*orderResource)(nil)
	_ resource.ResourceWithImportState = (*orderResource)(nil)
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
			for _, a := range []string{"root_volume_size", "data_volume"} {
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
	desc := r.spec.about + " Ordered from MTN Cloud's catalog (" + r.spec.item + "). An order is placed once: a " +
		"changed input is refused at plan time, and nothing is deleted to order it again. Change the plan, labels, security " +
		"groups, floating IP or data disks of the VMs it made with `mtncloud_instance_settings`. " +
		"`terraform apply -replace=<address>` orders it again; add `lifecycle { prevent_destroy = true }` to stop that. " +
		"The apply succeeds only when the order's workflow succeeded and every VM it made is running. The one exception: " +
		"when the workflow succeeded and only the lookup of what it made failed, the apply succeeds with a warning and " +
		"nothing is ordered again. `instance_ids` is then empty until the next refresh, so anything that indexes it in " +
		"the same apply fails until you apply again. Inputs are checked " +
		"at plan time against the item's live order form: what the form requires, its choices and its stated minimums. " +
		"`terraform destroy` deletes what it made, shown under " + where + " in the console, with its disks and floating " +
		"IPs, then the order's record in Catalog > Inventory. Secrets are write-only (Terraform 1.11 or newer): they are " +
		"sent with the order and never stored."
	if r.spec.heat {
		desc += " **Slow:** MTN's workflow builds the VM through a Heat stack, then waits for MTN Cloud to discover it, " +
			"so an order takes about an hour (the create timeout defaults to 3 hours). Use a login that outlasts the apply: " +
			"`username` and `password` (the provider logs in again when a token expires), or a token that lasts longer " +
			"than the apply; or order it in the console instead. Destroy deletes its VM, but not its Heat stack, " +
			"whose port and floating IP stay until MTN removes the stack."
	}
	if r.spec.reports {
		desc += " The order form doesn't let you name the VM: MTN names it from its template " +
			"(`<initials>-<account>-MTN-CLOUD-<type>-<n>`), as in the console, and the provider finds it from the " +
			"workflow's own report. `instance_names` shows the name it got."
	}
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
			Description:   "IDs of the instances the order created.",
			PlanModifiers: []planmodifier.List{listplanmodifier.UseStateForUnknown()}}
		if r.spec.app {
			attrs["app_id"] = schema.StringAttribute{Computed: true, PlanModifiers: keep,
				Description: "ID of the app the order made (under Apps in the console). Destroy deletes this app, even " +
					"after it is renamed; an instance added to it later is taken out of it first and left running."}
		}
		attrs["instance_names"] = schema.ListAttribute{Computed: true, ElementType: types.StringType,
			Description: "Names of the instances the order created, in the order of `instance_ids`. Where the form " +
				"names the VM itself, this is the name MTN gave it (`<initials>-<account>-MTN-CLOUD-<type>-<n>`).",
			PlanModifiers: []planmodifier.List{listplanmodifier.UseStateForUnknown()}}
		attrs["instance_statuses"] = schema.MapAttribute{Computed: true, ElementType: types.StringType,
			Description: "Status of each instance the order created, by instance ID, for example `running`, or `unknown` " +
				"until the next refresh when it couldn't be read. A `failed` instance plans a replace: the order is deleted " +
				"and placed again. An app order whose app couldn't be checked when it was placed, and then failed, shows it " +
				"here as `app-<id>`, and plans the same replace.",
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
			blocks[f.attr+"data_volume"] = schema.ListNestedBlock{
				Description: "Extra disks, in order. MTN's workflow puts every disk on the project's default datastore.",
				NestedObject: schema.NestedBlockObject{Attributes: map[string]schema.Attribute{
					"size": schema.Int64Attribute{Required: true, Description: "Size in GB.", Validators: []validator.Int64{int64validator.AtLeast(1)}},
				}},
			}
		default:
			attrs[f.attr] = r.attribute(f)
		}
	}
	desc += r.formSummary()
	resp.Schema = schema.Schema{Description: desc, Attributes: attrs, Blocks: blocks}
}

func (r *orderResource) attribute(f field) schema.Attribute {
	desc := f.desc
	if f.attr == "name" && r.spec.app {
		desc = "Name of the app the order creates, shown under Apps. It must be unique among your apps: the provider " +
			"finds the app, and the VMs in it, by this name (MTN names the VMs itself)."
	}
	d := desc + snapshotFor(r.spec.typeName).field(f.key).facts()
	// A required form field the form pre-fills is optional here: the live form's value is sent, as
	// the console does.
	req := f.required && !formFills(r.spec.typeName, f.key)
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
	d, lookupErr := r.client.DefaultsFor(ctx)
	for _, f := range r.spec.fields {
		switch f.kind {
		case kGroup:
			resp.Diagnostics.Append(planmodifiers.FillDefault(ctx, req.Config, &resp.Plan, f.attr, "MTNCLOUD_GROUP", d.Group, lookupErr)...)
		case kProject:
			resp.Diagnostics.Append(planmodifiers.FillDefault(ctx, req.Config, &resp.Plan, f.attr, "MTNCLOUD_PROJECT", d.Project, nil)...)
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
		// An order is placed once: a changed input is refused at plan time, never ordered again
		// (owner, 2026-10-31, superseding 2026-10-30). Day-2 changes to its VMs go through
		// mtncloud_instance_settings. -replace still orders it again, as it changes no input.
		replace := false
		for name, f := range r.inputs() {
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
			if r.spec.target && name == "instance_id" {
				// The parent (D-038): a target order follows its instance. Its destroy deletes nothing,
				// so it is placed again on the new one, as when the instance is replaced (its ID unknown).
				resp.RequiresReplace = append(resp.RequiresReplace, path.Root(name))
				replace = true
				continue
			}
			if after.IsUnknown() {
				continue // checked when Terraform plans again at apply, with the value known
			}
			r.refuseInput(name, f, before, &resp.Diagnostics)
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
				resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("instance_names"), types.ListUnknown(types.StringType))...)
			}
			if r.spec.app && r.appIDOf(ctx, req.State, &resp.Diagnostics) == 0 {
				resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("app_id"), types.StringUnknown())...)
			}
		}
		return
	}
	if !planKnown(ctx, resp.Plan, r.inputs()) {
		// Terraform plans again during apply once every input is known, so this runs then, before Create.
		return
	}
	r.build(ctx, resp.Plan, req.Config, true, &resp.Diagnostics) // resolves every name; errors point at the attribute
	if r.spec.target {
		r.checkTarget(ctx, resp.Plan, &resp.Diagnostics)
	}
}

// checkTarget checks, at plan, the instance a certificate is installed on: it must exist; it
// should be running, with a public IP the domain points at, for Let's Encrypt's HTTP challenge on
// port 80. Otherwise the order would only fail once placed.
func (r *orderResource) checkTarget(ctx context.Context, p source, diags *diag.Diagnostics) {
	var target, domain types.String
	diags.Append(p.GetAttribute(ctx, path.Root("instance_id"), &target)...)
	diags.Append(p.GetAttribute(ctx, path.Root("domain_name"), &domain)...)
	at := path.Root("instance_id")
	id, err := strconv.ParseInt(target.ValueString(), 10, 64)
	if err != nil {
		diags.AddAttributeError(at, "Invalid instance ID", fmt.Sprintf("%q is not an instance ID.", target.ValueString()))
		return
	}
	in, err := r.client.GetInstance(ctx, id)
	switch {
	case client.IsNotFound(err):
		diags.AddAttributeError(at, "No such instance",
			fmt.Sprintf("Instance %d doesn't exist, or this login can't see it. Nothing was ordered.", id))
		return
	case err != nil:
		diags.AddAttributeWarning(at, "Instance not checked", fmt.Sprintf("Instance %d couldn't be read: %s.", id, err))
		return
	}
	if !strings.EqualFold(in.Status, "running") {
		diags.AddAttributeWarning(at, "Instance not running",
			fmt.Sprintf("Instance %d (%s) is %q. The certificate is installed by logging in to it, so it must be running "+
				"when the order runs.", id, in.Name, in.Status))
	}
	if in.PublicIP == "" {
		diags.AddAttributeWarning(at, "Instance has no public IP",
			fmt.Sprintf("Instance %d (%s) has no floating IP. Let's Encrypt checks the domain over the internet on port 80, "+
				"so the order fails without one: attach one (mtncloud_instance_settings) and allow port 80 in its security group.", id, in.Name))
		return
	}
	if domain.IsNull() || domain.IsUnknown() {
		return
	}
	addrs, err := net.DefaultResolver.LookupHost(ctx, domain.ValueString())
	if err != nil || !slices.Contains(addrs, in.PublicIP) {
		diags.AddAttributeWarning(path.Root("domain_name"), "Domain doesn't point at the instance yet",
			fmt.Sprintf("%s doesn't resolve to instance %d's public IP yet. Let's Encrypt refuses the certificate until "+
				"it does: create the DNS record first, and allow port 80.", domain.ValueString(), id))
	}
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

// volume is an extra disk. It has no datastore: MTN's order workflows create every disk with
// datastoreId "auto" and pass the disk's storageType on as the volume type (CreateApacheInstance.sh
// and the other Create*.sh, 2026-11-02), so a datastore sent there was taken for a volume type.
type volume struct {
	Size types.Int64 `tfsdk:"size"`
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
		what := strings.ReplaceAll(strings.TrimSuffix(a, "_"), "_", " ")
		diags.AddAttributeError(path.Root(a), client.LookupTitle(err, what, "Invalid "+what), client.LookupDetail(err, what))
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
			if n := v.ValueInt64(); !v.IsNull() {
				if f.kind == kBytes {
					n *= 1 << 30
				}
				out[f.key] = n
			}
		case kFlag:
			var v types.Bool
			diags.Append(p.GetAttribute(ctx, path.Root(f.attr), &v)...)
			// A checkbox is sent as the console sends it, "on" or "off": MTN's scripts test
			// `== "on"`, and their `?: "on"` defaults turn a false into "on" (2026-10-31). Left out,
			// the live form decides (applyForm): its default, else "off", as the console sends.
			if !v.IsNull() {
				out[f.key] = checkbox(v.ValueBool())
			}
		case kList, kLabels:
			if v := strs(f.attr); len(v) > 0 {
				out[f.key] = strings.Join(v, ",")
			}
		case kLines:
			if v := strs(f.attr); len(v) > 0 {
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
				if len(data) > 0 {
					diags.AddAttributeError(path.Root(f.attr+"root_volume_size"), "Root disk size missing",
						"Set "+f.attr+"root_volume_size too: extra disks are ordered with the root disk.")
				}
				continue
			}
			if place == nil {
				continue
			}
			all := append([]volume{{Size: root}}, data...)
			var vols []any
			for i, v := range all {
				vol := map[string]any{"name": "root", "rootVolume": i == 0, "size": v.Size.ValueInt64()}
				if i > 0 {
					vol["name"] = "data-" + strconv.Itoa(i)
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
		case isErr: // a warning: the router may be moved to this pool in the same apply (B-020)
			diags.AddAttributeWarning(path.Root(attr), "Floating IP pool doesn't match the router", msg)
		default:
			diags.AddAttributeWarning(path.Root(attr), "Floating IP pool not checked against a router", msg)
		}
	}
	return out
}

// checkbox is a checkbox's value as the console sends it.
func checkbox(on bool) string {
	if on {
		return "on"
	}
	return "off"
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
	timeout, d := to.Create(ctx, r.createTimeout())
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
		setVMs(ctx, &resp.State, nil, nil, nil, &resp.Diagnostics)
	}
	if r.spec.app {
		resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("app_id"), types.StringNull())...)
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
		// A workflow can fail after its VM is up (MTN's SOPHOS workflow, QA 2026-10-31): kept in state,
		// tainted by the error, so the next apply deletes it before ordering again, and destroy deletes it.
		if left := r.leftovers(ctx, &resp.State, &resp.Diagnostics); len(left) > 0 {
			st, names := r.statusesOf(ctx, left)
			setVMs(ctx, &resp.State, left, st, names, &resp.Diagnostics)
			resp.Diagnostics.AddError(r.spec.item+" order failed",
				fmt.Sprintf("Order %d ended with %q%s. It left instance %s, kept in state: the next apply deletes it and orders "+
					"again, and terraform destroy deletes it.", item.ID, executionStatus(item), why, joinIDs(left)))
			return
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
	if r.spec.app {
		a, ok := r.appReady(ctx, req.Plan, item.ID, before, &resp.Diagnostics)
		if a != nil {
			resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("app_id"), strconv.FormatInt(a.ID, 10))...)
		}
		if !ok {
			if !resp.Diagnostics.HasError() {
				// Only the lookup failed, so the app hasn't been seen to come up: Read checks it (D-044).
				resp.Diagnostics.Append(resp.Private.SetKey(ctx, appUnchecked, []byte("true"))...)
			}
			return
		}
	}
	// Success is what the order made, running (owner, 2026-10-30): wait for its VM, within the
	// create timeout. A VM can show up after the workflow ends, and some workflows name it
	// themselves (SOPHOS: "<initials>-<account>-MTN-CLOUD-SPH001-1", QA 2026-10-31).
	var ids []int64
	werr := r.client.Await(ctx, func(ctx context.Context) (bool, error) {
		var err error
		ids, err = r.instancesNamed(ctx, resp.State, &resp.Diagnostics)
		return len(ids) > 0, err
	})
	if r.lookupOnlyFailed(ctx, werr, item.ID, &resp.Diagnostics) {
		return
	}
	if werr != nil && r.spec.reports {
		resp.Diagnostics.AddError(r.spec.item+" instance not found",
			fmt.Sprintf("Order %d's workflow succeeded, but the VM it reported isn't in the project (%s). MTN's workflow names "+
				"the VM itself (<initials>-<account>-MTN-CLOUD-<type>-<n>): check Instances in the console. The next apply "+
				"orders it again.", item.ID, werr))
		return
	}
	if werr != nil {
		name := r.nameOf(ctx, req.Plan, &resp.Diagnostics)
		resp.Diagnostics.AddError(r.spec.item+" instance not found",
			fmt.Sprintf("Order %d's workflow succeeded, but no instance named %q is in the project (%s). MTN's workflow may "+
				"have named it itself: check Instances in the console. The next apply deletes the order and orders it again; "+
				"raise timeouts.create if the VM is only slow to appear.", item.ID, name.ValueString(), werr))
		return
	}
	statuses, names := map[string]string{}, map[string]string{}
	for _, id := range ids {
		in, err := r.client.WaitForInstance(ctx, id)
		key := strconv.FormatInt(id, 10)
		if in != nil {
			names[key] = in.Name
		}
		switch {
		case r.lookupOnlyFailed(ctx, err, item.ID, &resp.Diagnostics):
			statuses[key] = "unknown"
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
	setVMs(ctx, &resp.State, ids, statuses, names, &resp.Diagnostics)
}

// lookupOnlyFailed warns, and reports true, when the order's workflow succeeded and only looking for
// what it made failed (not a timeout). That order worked: it stays in state with no error, so
// nothing tainted deletes a good VM and orders another. The next refresh finds the VM (D-044).
func (r *orderResource) lookupOnlyFailed(ctx context.Context, err error, orderID int64, diags *diag.Diagnostics) bool {
	if err == nil || ctx.Err() != nil || !client.LookupFailed(err) {
		return false
	}
	diags.AddWarning("Couldn't look up what "+r.spec.item+" made",
		fmt.Sprintf("Order %d's workflow succeeded, but looking up what it made failed: %s. Whether it came up isn't known "+
			"yet: check Instances or Apps in the console. The order is saved and won't be ordered again. The next refresh looks "+
			"again and fills in instance_ids; run terraform refresh or plan.", orderID, err))
	return true
}

// createTimeout is the default create timeout: a workflow that builds through a Heat stack waits for
// MTN's cloud sync, about an hour (SOPHOS script: "EXPECTED_MINUTES" 60, no limit of its own).
func (r *orderResource) createTimeout() time.Duration {
	if r.spec.heat {
		return 3 * time.Hour
	}
	return orderTimeout
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
	name := r.nameOf(ctx, p, diags)
	ids, err := r.instancesNamed(ctx, p, diags)
	if err != nil {
		diags.AddWarning("Unable to find the order's instances", err.Error()+". The next refresh looks again.")
		return nil
	}
	if len(ids) == 0 {
		what := fmt.Sprintf("no instance named %q is in the project yet", name.ValueString())
		if r.spec.reports {
			what = "the VM its workflow reported isn't in the project yet"
		}
		diags.AddWarning("No instance found for "+r.spec.item, "The order succeeded, but "+what+". The next refresh looks again.")
	}
	return ids
}

// instancesNamed lists the instances in the order's project named after it (madeBy).
func (r *orderResource) instancesNamed(ctx context.Context, p source, diags *diag.Diagnostics) ([]int64, error) {
	var project types.String
	name := r.nameOf(ctx, p, diags)
	diags.Append(p.GetAttribute(ctx, path.Root("project"), &project)...)
	all, err := r.client.ListInstances(ctx)
	if client.IsUnreadable(err) && !r.spec.app {
		// One instance the list can't decode mustn't hide this one, so ask for it alone. An app's VMs
		// have template names, so those can't be. A reported VM is read from what its workflow printed.
		all, err = nil, nil
		if !r.spec.reports {
			all, err = r.client.ListInstancesNamed(ctx, name.ValueString())
		}
	}
	if err != nil {
		return nil, fmt.Errorf("the instances couldn't be listed: %w", err)
	}
	pr, err := r.client.ProjectByName(ctx, project.ValueString())
	if err != nil {
		return nil, fmt.Errorf("the project couldn't be read: %w", err)
	}
	if r.spec.reports {
		return r.reportedInstance(ctx, p, pr.ID, all, diags)
	}
	var ids []int64
	inProject := map[int64]bool{}
	for _, in := range all {
		inProject[in.ID] = in.ProjectID == pr.ID
		if in.ProjectID == pr.ID && madeBy(in.Name, name.ValueString()) {
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
		// The saved app first: it may have been renamed since (B-028 catch 1).
		mine := client.AppsNamed(apps, name.ValueString())
		if appID := r.appIDOf(ctx, p, diags); appID != 0 {
			mine = nil
			for _, a := range apps {
				if a.ID == appID {
					mine = append(mine, a)
				}
			}
		}
		// Instances added to the app later (mtncloud_app_instance, the console) aren't the order's: its
		// own carry MTN's template name or the order's. Only when none does are all members taken, so
		// the order's VMs are never lost (review, 2026-10-31).
		names := map[int64]string{}
		for _, in := range all {
			names[in.ID] = in.Name
		}
		var members, own []int64
		for _, a := range mine {
			for _, id := range a.InstanceIDs {
				if !inProject[id] || slices.Contains(ids, id) || slices.Contains(members, id) {
					continue
				}
				members = append(members, id)
				if templateNamed(names[id]) {
					own = append(own, id)
				}
			}
		}
		if len(own) == 0 && len(ids) == 0 {
			own = members
		}
		ids = append(ids, own...)
	}
	return ids, nil
}

// nameOf is the order's name argument: null for an item whose VM MTN names itself (spec reports),
// which has no name argument.
func (r *orderResource) nameOf(ctx context.Context, p source, diags *diag.Diagnostics) types.String {
	if r.spec.reports {
		return types.StringNull()
	}
	var name types.String
	diags.Append(p.GetAttribute(ctx, path.Root("name"), &name)...)
	return name
}

// templateNamed reports whether an instance has the name MTN's order workflows give their VMs,
// "<initials>-<account>-MTN-CLOUD-<type>-<n>".
func templateNamed(name string) bool {
	return strings.Contains(strings.ToUpper(name), "-MTN-CLOUD-")
}

// appIDOf is the app ID saved in p, 0 when there is none (an older state, or not an app order).
func (r *orderResource) appIDOf(ctx context.Context, p source, diags *diag.Diagnostics) int64 {
	if !r.spec.app {
		return 0
	}
	var v types.String
	diags.Append(p.GetAttribute(ctx, path.Root("app_id"), &v)...)
	id, _ := strconv.ParseInt(v.ValueString(), 10, 64)
	return id
}

// appFor is the order's app, for a state that has none saved (made before app_id, or imported):
// the app named after the order that holds one of its instances. Null when there is none.
func (r *orderResource) appFor(ctx context.Context, p source, ids []int64, diags *diag.Diagnostics) types.String {
	name := r.nameOf(ctx, p, diags)
	apps, err := r.client.ListApps(ctx)
	if err != nil || name.IsNull() {
		return types.StringNull()
	}
	for _, a := range client.AppsNamed(apps, name.ValueString()) {
		for _, id := range a.InstanceIDs {
			if slices.Contains(ids, id) {
				return types.StringValue(strconv.FormatInt(a.ID, 10))
			}
		}
	}
	return types.StringNull()
}

// savedApp reads the app in state. Only that one: app names repeat, and an app doesn't say which
// project it is in, so one is the order's only when it holds the order's VMs (appFor). A name alone
// could take another project's app, which destroy would then delete. Nil until it is known and read.
func (r *orderResource) savedApp(ctx context.Context, p source, diags *diag.Diagnostics) *client.App {
	id := r.appIDOf(ctx, p, diags)
	if id == 0 {
		return nil
	}
	apps, err := r.client.ListApps(ctx)
	if err != nil {
		diags.AddWarning("Unable to check the order's app", err.Error()+". The next refresh looks again.")
		return nil
	}
	for _, a := range apps {
		if a.ID == id {
			return &a
		}
	}
	return nil
}

// reportedInstance is the VM a workflow that names it itself reported (spec reports): by the ID it
// printed, else by the name, and only in the order's project. Nothing before the order exists.
func (r *orderResource) reportedInstance(ctx context.Context, p source, projectID int64, all []*client.Instance, diags *diag.Diagnostics) ([]int64, error) {
	var idv types.String
	diags.Append(p.GetAttribute(ctx, path.Root("id"), &idv)...)
	if idv.IsNull() || idv.IsUnknown() {
		return nil, nil // not ordered yet
	}
	orderID, err := strconv.ParseInt(idv.ValueString(), 10, 64)
	if err != nil {
		return nil, fmt.Errorf("%q is not an order ID", idv.ValueString())
	}
	item, err := r.client.GetOrderedItem(ctx, orderID)
	if err != nil {
		return nil, fmt.Errorf("the order couldn't be read: %w", err)
	}
	if item.Execution == nil || item.Execution.ID == 0 {
		return nil, nil
	}
	out, err := r.client.ExecutionOutput(ctx, item.Execution.ID)
	if err != nil {
		return nil, fmt.Errorf("the order's workflow output couldn't be read: %w", err)
	}
	id, name := client.WorkflowInstance(out)
	if all == nil { // the list couldn't be read
		switch {
		case id != 0:
			in, err := r.client.GetInstance(ctx, id)
			if client.IsNotFound(err) {
				return nil, nil
			}
			if err != nil {
				return nil, err
			}
			all = []*client.Instance{in}
		case name != "":
			if all, err = r.client.ListInstancesNamed(ctx, name); err != nil {
				return nil, err
			}
		}
	}
	for _, in := range all {
		if in.ProjectID == projectID && ((id != 0 && in.ID == id) || (id == 0 && name != "" && strings.EqualFold(in.Name, name))) {
			return []int64{in.ID}, nil
		}
	}
	return nil, nil
}

// appsBefore notes the apps with the order's name before it is placed, so the wait never takes
// one of them for the new app. One already in the order's project stops the order: its VMs would
// be taken for this order's, and deleted with it.
func (r *orderResource) appsBefore(ctx context.Context, p source, diags *diag.Diagnostics) (map[int64]bool, bool) {
	name := r.nameOf(ctx, p, diags)
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
// ends once it has asked for the app. False, with an error, when it didn't come up running; the
// app, when one appeared, either way (it is the order's, and destroy deletes it).
func (r *orderResource) appReady(ctx context.Context, p source, orderID int64, before map[int64]bool, diags *diag.Diagnostics) (*client.App, bool) {
	name := r.nameOf(ctx, p, diags)
	a, err := r.client.WaitForApp(ctx, name.ValueString(), before)
	switch {
	case r.lookupOnlyFailed(ctx, err, orderID, diags):
		return a, false
	case err != nil && a == nil && errors.Is(err, context.DeadlineExceeded):
		diags.AddError(r.spec.item+" app not found",
			fmt.Sprintf("Order %d's workflow succeeded, but no app named %q appeared before the create timeout. Check Apps "+
				"in the console; the next apply deletes the order and orders it again.", orderID, name.ValueString()))
		return nil, false
	case err != nil:
		diags.AddError(r.spec.item+" app not ready",
			fmt.Sprintf("App %q from order %d didn't finish provisioning: %s. The next apply deletes the order with what it "+
				"made and orders it again; raise timeouts.create if it is only slow.", name.ValueString(), orderID, err))
		return a, false
	case !client.AppRunning(a):
		diags.AddError(r.spec.item+" app failed",
			fmt.Sprintf("App %q (%d) from order %d is %q. The order's workflow succeeded, but what it made didn't. Check it "+
				"under Apps; the next apply deletes the order with what it made and orders it again.", a.Name, a.ID, orderID, a.Status))
		return a, false
	}
	return a, true
}

// madeBy reports whether an instance is named after an order: exactly "<name>", case ignored. A
// multi-node app's VMs are named from MTN's template instead ("<prefix>-MTN-CLOUD-MK8S-M-2", live QA
// 2026-10-31, B-018 #2) and are found through the app. "<name>-2" is never claimed: it may be another
// order's VM or the user's own, and destroy deletes what this matches.
func madeBy(instance, order string) bool {
	return strings.EqualFold(instance, order)
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
		if left := r.leftovers(ctx, req.State, &resp.Diagnostics); len(left) > 0 {
			// It failed after making something: kept, so the next apply deletes it before ordering again.
			resp.Diagnostics.AddWarning(r.spec.item+" order failed",
				fmt.Sprintf("Order %d ended with %q%s, and left instance %s. The next apply deletes it and orders again; "+
					"terraform destroy deletes it.", id, executionStatus(item), message(item), joinIDs(left)))
			resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("status"), executionStatus(item))...)
			st, names := r.statusesOf(ctx, left)
			setVMs(ctx, &resp.State, left, st, names, &resp.Diagnostics)
			return
		}
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
	statuses, names := map[string]string{}, map[string]string{}
	if len(ids) == 0 {
		name := r.nameOf(ctx, req.State, &resp.Diagnostics)
		if !name.IsNull() || r.spec.reports {
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
				statuses[s], names[s] = in.Status, in.Name
			}
		}
		if len(found) == 0 {
			// Everything it made was deleted: plan a new order (rule 8).
			resp.State.RemoveResource(ctx)
			return
		}
	}
	if len(ids) == 0 {
		statuses, names = r.statusesOf(ctx, found)
	}
	if r.spec.app && r.appIDOf(ctx, req.State, &resp.Diagnostics) == 0 && len(found) > 0 {
		resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("app_id"), r.appFor(ctx, req.State, found, &resp.Diagnostics))...)
	}
	unchecked, d := req.Private.GetKey(ctx, appUnchecked)
	resp.Diagnostics.Append(d...)
	if r.spec.app && len(unchecked) > 0 {
		if a := r.savedApp(ctx, &resp.State, &resp.Diagnostics); a != nil {
			appID := strconv.FormatInt(a.ID, 10)
			switch {
			case !client.AppSettled(a):
			case client.AppRunning(a):
				resp.Diagnostics.Append(resp.Private.SetKey(ctx, appUnchecked, nil)...)
			default:
				// Still unchecked, so every plan shows it until it is replaced or comes back. A status
				// the replace check reads: an app can fail with no VM to show it.
				statuses["app-"+appID] = "failed"
				resp.Diagnostics.AddWarning(r.spec.item+" app failed",
					fmt.Sprintf("App %q (%d) from order %d is %q. The order's workflow succeeded, but what it made didn't. The "+
						"next apply deletes the order with what it made and orders it again.", a.Name, a.ID, id, a.Status))
			}
		}
	}
	setVMs(ctx, &resp.State, found, statuses, names, &resp.Diagnostics)
}

// statusesOf reads each instance's status; one that can't be read is left out.
// leftovers are the instances a failed order made that still exist: its saved instance_ids, else
// those named after it (or, for a heat order, the one its workflow reported) in its project. None
// when they can't be looked up, for example after an import, where the project isn't known yet.
func (r *orderResource) leftovers(ctx context.Context, st source, diags *diag.Diagnostics) []int64 {
	if r.spec.target {
		return nil
	}
	var ids []string
	diags.Append(st.GetAttribute(ctx, path.Root("instance_ids"), &ids)...)
	var out []int64
	for _, s := range ids {
		n, _ := strconv.ParseInt(s, 10, 64)
		if _, err := r.client.GetInstance(ctx, n); err == nil {
			out = append(out, n)
		}
	}
	if len(ids) > 0 {
		return out
	}
	var project types.String
	name := r.nameOf(ctx, st, diags)
	diags.Append(st.GetAttribute(ctx, path.Root("project"), &project)...)
	if project.ValueString() == "" || (name.ValueString() == "" && !r.spec.reports) {
		return nil
	}
	found, err := r.instancesNamed(ctx, st, &diag.Diagnostics{})
	if err != nil {
		return nil
	}
	return found
}

// statusesOf reads each instance's status and name, by ID; one that can't be read is left out.
func (r *orderResource) statusesOf(ctx context.Context, ids []int64) (statuses, names map[string]string) {
	statuses, names = map[string]string{}, map[string]string{}
	for _, id := range ids {
		if in, err := r.client.GetInstance(ctx, id); err == nil {
			key := strconv.FormatInt(id, 10)
			statuses[key], names[key] = in.Status, in.Name
		}
	}
	return statuses, names
}

// setVMs saves what an order made: its instance IDs, and their statuses and names by ID.
func setVMs(ctx context.Context, st *tfsdk.State, ids []int64, statuses, names map[string]string, diags *diag.Diagnostics) {
	list := make([]attr.Value, 0, len(ids))
	for _, id := range ids {
		list = append(list, types.StringValue(names[strconv.FormatInt(id, 10)]))
	}
	diags.Append(st.SetAttribute(ctx, path.Root("instance_ids"), idList(ids))...)
	diags.Append(st.SetAttribute(ctx, path.Root("instance_statuses"), statusMap(statuses))...)
	diags.Append(st.SetAttribute(ctx, path.Root("instance_names"), types.ListValueMust(types.StringType, list))...)
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
	statuses, names := map[string]string{}, map[string]string{}
	if r.ready(&resp.Diagnostics) {
		statuses, names = r.statusesOf(ctx, found)
	}
	if r.spec.app {
		appID := types.StringNull()
		resp.Diagnostics.Append(req.State.GetAttribute(ctx, path.Root("app_id"), &appID)...)
		if appID.IsNull() && len(found) > 0 && r.ready(&resp.Diagnostics) {
			appID = r.appFor(ctx, req.Plan, found, &resp.Diagnostics)
		}
		resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("app_id"), appID)...)
	}
	setVMs(ctx, &resp.State, found, statuses, names, &resp.Diagnostics)
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
	name = r.nameOf(ctx, req.State, &resp.Diagnostics)
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
	if name.IsNull() && !r.spec.reports {
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
	plan := deletePlan(apps, ids, name.ValueString(), r.spec.app, r.appIDOf(ctx, req.State, &resp.Diagnostics))
	// No execution on the record doesn't mean nothing was made (FortiADC QA, 2026-10-31: its record
	// showed none while its workflow had made a VM), so nothing found is always refused, never
	// taken as withdrawn: deleting the record would orphan what the workflow made.
	if len(plan.apps) == 0 && len(plan.instances) == 0 && r.spec.reports {
		refuse("Nothing it made was found: the VM its workflow reported isn't in its project. Check Instances in the console.")
		return
	}
	if len(plan.apps) == 0 && len(plan.instances) == 0 {
		refuse(fmt.Sprintf("Nothing it made was found: no instance named %q is in its project%s. Check it in the console.",
			name.ValueString(), map[bool]string{true: ", and no app has that name", false: ""}[r.spec.app]))
		return
	}
	var stacks []string // a Heat stack's name is its VM's, with "-stack"
	for _, id := range plan.instances {
		if in, err := r.client.GetInstance(ctx, id); r.spec.heat && err == nil {
			stacks = append(stacks, in.Name+"-stack")
		}
	}
	// Instances added to the order's app since are someone else's: out of the app first, left
	// running (B-028 catch 2, option a).
	for _, d := range plan.detach {
		if err := r.client.RemoveAppInstance(ctx, d[0], d[1]); err != nil {
			resp.Diagnostics.AddError("Unable to take instance "+strconv.FormatInt(d[1], 10)+" out of app "+strconv.FormatInt(d[0], 10),
				fmt.Sprintf("Order %d's app holds instance %d, which the order didn't make, and it couldn't be taken out: %s. "+
					"Nothing was deleted. Remove it from the app in the console, then destroy again.", orderID, d[1], err))
			return
		}
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
	if r.spec.heat {
		resp.Diagnostics.AddWarning("Heat stack left in MTN Cloud",
			fmt.Sprintf("Order %d's VM is deleted, but MTN's workflow built it through a Heat stack (%s), which a customer "+
				"login can't delete: its network port and floating IP stay, and keep the router from being deleted. Ask MTN "+
				"to delete the stack.", orderID, strings.Join(stacks, ", ")))
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
	detach                [][2]int64 // {app, instance}: taken out of the order's app before it is deleted
}

// deletePlan decides how an order's instances are deleted. An app order's own app (the saved
// appID, else named after the order and holding at least one of its instances) is deleted with the
// order's instances in it; any other instance in it is taken out first and left running. Any other
// app goes whole only when every instance in it is the order's: an app that also holds someone
// else's instance is left, and the order's instances in it are deleted one by one.
func deletePlan(apps []client.App, ids []int64, name string, app bool, appID int64) orderDeletion {
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
		own := app && ((appID != 0 && a.ID == appID) || (appID == 0 && holds && strings.EqualFold(a.Name, name)))
		if !own && (!holds || !all) {
			continue
		}
		out.apps = append(out.apps, a.ID)
		for _, id := range a.InstanceIDs {
			switch {
			case !mine[id]:
				out.detach = append(out.detach, [2]int64{a.ID, id})
			case !covered[id]:
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

// joinIDs is a list of instance IDs for a message, for example "4641, 4642".
func joinIDs(ids []int64) string {
	out := make([]string, len(ids))
	for i, id := range ids {
		out[i] = strconv.FormatInt(id, 10)
	}
	return strings.Join(out, ", ")
}

// day2 are the order inputs mtncloud_instance_settings changes on the order's VMs.
var day2 = map[kind]string{kPlan: "plan", kLabels: "labels", kSecGroups: "security_groups", kFloatingIP: "floating_ip_pool"}

// refuseInput is the plan-time error for a changed input. A secret's value is never shown.
func (r *orderResource) refuseInput(name string, f field, before attr.Value, diags *diag.Diagnostics) {
	fix := "Set it back to " + before.String() + "."
	switch {
	case f.kind == kSecret || f.kind == kExtra: // extra_inputs is sensitive: a form field there can be a password
		fix = "Set it back to the value the order was placed with."
	case before.IsNull():
		fix = "Remove it: the order was placed without it."
	}
	how := "To get a new one, remove this order from the configuration (which deletes what it made), then add it again; " +
		"or run `terraform apply -replace=<address>`."
	if a, ok := day2[f.kind]; ok && !r.spec.target {
		how = fmt.Sprintf("To change %s on the VMs it made, without ordering again, use `mtncloud_instance_settings` "+
			"on each VM to change (`instance_id` = one of this order's `instance_ids`, `%s = ...`).", a, a)
	}
	if name == "data_volume" && !r.spec.target {
		how = "To add, grow or remove a data disk on the VMs it made, without ordering again, use `mtncloud_instance_settings` " +
			"on each VM to change (`instance_id` = one of this order's `instance_ids`, with `data_volume` blocks)."
	}
	diags.AddAttributeError(path.Root(name), name+" can't change",
		fmt.Sprintf("A %s order is placed once, and Terraform won't delete what it made to order it again. %s %s",
			r.spec.item, fix, how))
}
