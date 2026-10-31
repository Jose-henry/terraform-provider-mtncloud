package compute

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-framework-timeouts/resource/timeouts"
	"github.com/hashicorp/terraform-plugin-framework-validators/float64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/listvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/boolplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64default"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/listplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/mapplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-framework/types/basetypes"

	"github.com/mtn/terraform-provider-mtncloud/internal/client"
	"github.com/mtn/terraform-provider-mtncloud/internal/normalize"
)

// Provisioning a VM takes minutes; these bound a stuck one.
const instanceTimeout = 30 * time.Minute

// MTN's VM Provisioning Workflow: Windows Server needs a root volume of at least 40 GB.
const windowsMinRootGB = 40

var (
	digits   = regexp.MustCompile(`^[1-9][0-9]*$`) // an ID: never 0, never zero-padded
	hostname = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?$`)
)

var (
	_ resource.ResourceWithConfigure   = (*instanceResource)(nil)
	_ resource.ResourceWithImportState = (*instanceResource)(nil)
	_ resource.ResourceWithModifyPlan  = (*instanceResource)(nil)
)

func newInstance() resource.Resource { return &instanceResource{} }

type instanceResource struct{ base }

type instanceModel struct {
	ID             types.String   `tfsdk:"id"`
	Name           types.String   `tfsdk:"name"`
	Hostname       types.String   `tfsdk:"hostname"`
	Description    types.String   `tfsdk:"description"`
	Group          types.String   `tfsdk:"group"`
	Project        types.String   `tfsdk:"project"`
	Image          types.String   `tfsdk:"image"`
	Plan           types.String   `tfsdk:"plan"`
	Networks       types.List     `tfsdk:"networks"`
	SecurityGroups types.Set      `tfsdk:"security_groups"`
	RootVolumeSize types.Int64    `tfsdk:"root_volume_size"`
	Datastore      types.String   `tfsdk:"datastore"`
	DataVolumes    types.List     `tfsdk:"data_volume"`
	Environment    types.String   `tfsdk:"environment"`
	Labels         types.Set      `tfsdk:"labels"`
	Tags           types.Map      `tfsdk:"tags"`
	EnvVars        types.Map      `tfsdk:"env_vars"`
	Domain         types.String   `tfsdk:"domain"`
	InstallAgent   types.Bool     `tfsdk:"install_agent"`
	CreateUser     types.Bool     `tfsdk:"create_user"`
	ScaleFactor    types.Int64    `tfsdk:"scale_factor"`
	Scale          types.Object   `tfsdk:"scale"`
	FloatingIPPool types.String   `tfsdk:"floating_ip_pool"`
	ExpireDays     types.Int64    `tfsdk:"expire_days"`
	ShutdownDays   types.Int64    `tfsdk:"shutdown_days"`
	ExpireDate     types.String   `tfsdk:"expire_date"`
	ShutdownDate   types.String   `tfsdk:"shutdown_date"`
	PowerState     types.String   `tfsdk:"power_state"`
	RestartTrigger types.String   `tfsdk:"restart_trigger"`
	Status         types.String   `tfsdk:"status"`
	PrivateIP      types.String   `tfsdk:"private_ip"`
	PublicIP       types.String   `tfsdk:"public_ip"`
	PublicIPs      types.List     `tfsdk:"public_ips"`
	Timeouts       timeouts.Value `tfsdk:"timeouts"`
}

type dataVolumeModel struct {
	Name      types.String `tfsdk:"name"`
	Size      types.Int64  `tfsdk:"size"`
	Datastore types.String `tfsdk:"datastore"`
}

type scaleModel struct {
	MinCount  types.Int64  `tfsdk:"min_count"`
	MaxCount  types.Int64  `tfsdk:"max_count"`
	Increment types.Int64  `tfsdk:"increment"`
	AutoUp    types.Bool   `tfsdk:"auto_up"`
	AutoDown  types.Bool   `tfsdk:"auto_down"`
	CPU       types.Object `tfsdk:"cpu"`
	Memory    types.Object `tfsdk:"memory"`
	Disk      types.Object `tfsdk:"disk"`
}

type rangeModel struct {
	Min types.Float64 `tfsdk:"min"`
	Max types.Float64 `tfsdk:"max"`
}

var (
	rangeType = types.ObjectType{AttrTypes: map[string]attr.Type{"min": types.Float64Type, "max": types.Float64Type}}
	scaleType = types.ObjectType{AttrTypes: map[string]attr.Type{
		"min_count": types.Int64Type, "max_count": types.Int64Type, "increment": types.Int64Type,
		"auto_up": types.BoolType, "auto_down": types.BoolType, "cpu": rangeType, "memory": rangeType, "disk": rangeType,
	}}
)

var dataVolumeType = types.ObjectType{AttrTypes: map[string]attr.Type{
	"name": types.StringType, "size": types.Int64Type, "datastore": types.StringType,
}}

func (r *instanceResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_instance"
}

func (r *instanceResource) Schema(ctx context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	keep := []planmodifier.String{stringplanmodifier.UseStateForUnknown()}
	resp.Schema = schema.Schema{
		Description: "An MTN Cloud instance: a virtual machine in one of your projects. Creating it waits until " +
			"MTN Cloud reports it running, usually a few minutes. The project's default security group is always applied, " +
			"as MTN Cloud requires.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{Computed: true, Description: "Instance ID.", PlanModifiers: keep},
			// Optional+Computed: left out, the tenant's naming template names the instance and the name is read back.
			"name": schema.StringAttribute{
				Optional: true, Computed: true,
				Description: "Name of the instance. Leave it out to use your tenant's naming template (for example " +
					"`<initials>-<account>-MTN-CLOUD-<type>-<n>`); the generated name is then shown here. Renaming is done in place.",
				Validators:    []validator.String{stringvalidator.LengthBetween(1, 255)},
				PlanModifiers: keep,
			},
			// Optional+Computed: the hostname policy (or the name) sets it when left out.
			"hostname": schema.StringAttribute{
				Optional: true, Computed: true,
				Description: "Hostname set inside the VM. Left out, MTN Cloud derives it from the name or your hostname " +
					"template. Changing it replaces the instance.",
				Validators: []validator.String{stringvalidator.RegexMatches(hostname,
					"must be letters, digits and hyphens, at most 63 characters, not starting or ending with a hyphen")},
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown(), stringplanmodifier.RequiresReplaceIfConfigured()},
			},
			"description": schema.StringAttribute{Optional: true, Description: "Description of the instance."},
			// Optional+Computed (group and project): left out, the provider's value is used, and state keeps it.
			"group": schema.StringAttribute{
				Optional: true, Computed: true,
				Description: "Group the instance is provisioned into, by name, for example `MTNNG_CLOUD_AZ_1`. Defaults to the " +
					"provider's `group`. Changing it, or that default when you rely on it, replaces the instance.",
			},
			"project": schema.StringAttribute{
				Optional: true, Computed: true,
				Description: "Project the instance runs in, by name (for example `web-prod`; the full name with the tenant " +
					"suffix also works). Defaults to the provider's `project`. Changing it to another project, or that default when you rely on it, replaces the instance.",
			},
			"image": schema.StringAttribute{
				Required: true,
				Description: "Operating system image, by name (`MTN Ubuntu Server 24.04.3LTS`) or code (`MTN-U24.04LTS`); " +
					"the `mtncloud_images` data source lists them. Changing it replaces the instance.",
			},
			"plan": schema.StringAttribute{
				Required: true,
				Description: "Size of the VM, for example `G2S4` (the `mtncloud_plans` data source lists a project's sizes). " +
					"**Changing it restarts the VM**, in place.",
			},
			"networks": schema.ListAttribute{
				Required: true, ElementType: types.StringType,
				Description: "IDs of the MTN Networks to connect, for example `[mtncloud_network.app.id]`. The first is the " +
					"primary network. Any change replaces the instance: MTN Cloud doesn't report an instance's network card IDs, " +
					"so a network can't be added to a running instance safely. Create the networks first: an ID known at plan " +
					"time must be a network in the instance's project, or the plan fails.",
				Validators: []validator.List{
					listvalidator.SizeAtLeast(1),
					listvalidator.UniqueValues(),
					listvalidator.ValueStringsAre(stringvalidator.RegexMatches(digits, "must be a network ID")),
				},
			},
			"security_groups": schema.SetAttribute{
				Optional: true, ElementType: types.StringType,
				Description: "Security groups to apply, by name, in addition to the project's default group, which is " +
					"always applied; listing the default too changes nothing. Changed in place. Each group needs the project as a location.",
			},
			// Required (owner, 2026-10-31): every VM states its disk, as the console asks for one.
			"root_volume_size": schema.Int64Attribute{
				Required: true,
				Description: "Root volume size in GB. MTN Cloud can't resize a root volume: add a `data_volume` instead. " +
					"Windows images need at least 40 GB. Upgrading from 0.7.0, where it was optional, set it to the size " +
					"the instance has (shown in state): any other size is refused, as a root disk can't be resized.",
				Validators: []validator.Int64{int64validator.AtLeast(1)},
			},
			"datastore": schema.StringAttribute{
				Optional: true,
				Description: "Datastore of the root volume, by name, for example `__DEFAULT__` (the `mtncloud_datastores` " +
					"data source lists them). Left out, the project's default. Changing it replaces the instance.",
			},
			"environment": schema.StringAttribute{
				Optional:    true,
				Description: "Environment, by name or code, for example `Production`. Changed in place.",
			},
			"labels": schema.SetAttribute{
				Optional: true, ElementType: types.StringType,
				Description: "Labels (keywords). Changed in place.",
			},
			"tags": schema.MapAttribute{
				Optional: true, ElementType: types.StringType,
				Description: "Metadata tags, name to value. Changed in place.",
			},
			"env_vars": schema.MapAttribute{
				Optional: true, Sensitive: true, ElementType: types.StringType,
				Description: "Environment variables for the instance's automation, name to value. Sensitive: values are " +
					"hidden in plans, but stored in state. The variables MTN Cloud adds itself (address, host, ports) are " +
					"not shown. Changing them replaces the instance.",
				PlanModifiers: []planmodifier.Map{mapplanmodifier.RequiresReplace()},
			},
			// Optional+Computed: MTN Cloud may give the instance its network's domain when none is set.
			"domain": schema.StringAttribute{
				Optional: true, Computed: true,
				Description: "Network domain the instance joins, by name, for example `example.com`. Left out, MTN Cloud " +
					"may apply the network's domain, shown here. Changing it replaces the instance.",
				PlanModifiers: keep,
			},
			// Optional+Computed: left out, it follows the primary network's router at create (owner,
			// 2026-10-30), then stays as it was set.
			"install_agent": schema.BoolAttribute{
				Optional: true, Computed: true,
				Description: "Install the MTN Cloud agent, for monitoring, logs and automation. The agent needs the primary " +
					"network to be on an MTN Router with an external network, so it can reach MTN Cloud: without one, the VM " +
					"stays \"provisioning\" (finalizing) until the create timeout. Left out, it is `true` when the primary " +
					"network is on such a router and `false` otherwise, decided when the instance is created (the plan shows " +
					"it as known after apply) and kept after. " +
					"Changing it replaces the instance.",
				PlanModifiers: []planmodifier.Bool{boolplanmodifier.UseStateForUnknown(), boolplanmodifier.RequiresReplace()},
			},
			"create_user": schema.BoolAttribute{
				Optional: true, Computed: true, Default: booldefault.StaticBool(true),
				Description: "Create your user (from your Linux or Windows user settings) on the VM. Defaults to `true`. " +
					"Changing it replaces the instance.",
				PlanModifiers: []planmodifier.Bool{boolplanmodifier.RequiresReplace()},
			},
			"scale_factor": schema.Int64Attribute{
				Optional: true, Computed: true, Default: int64default.StaticInt64(1),
				Description: "Number of VMs in the instance. Defaults to 1. For several separate instances use `count` or " +
					"`for_each`. Changing it replaces the instance.",
				Validators:    []validator.Int64{int64validator.Between(1, 20)},
				PlanModifiers: []planmodifier.Int64{int64planmodifier.RequiresReplace()},
			},
			"scale": scaleAttribute("Automatic scaling: MTN Cloud adds a VM when usage stays above a metric's `max` and removes one " +
				"below its `min`, between `min_count` and `max_count`. Watch at least one of `cpu`, `memory`, `disk`. " +
				"Changed in place; removing it turns scaling off."),
			"floating_ip_pool": schema.StringAttribute{
				// Kept as configured while the instance has an address: a customer can't read which pool it
				// came from. No address means none is attached.
				Optional: true,
				Description: "Give the instance a floating (public) IP from this pool, for example `public-network-02` (the " +
					"`mtncloud_external_networks` data source lists the names). It is attached once the VM is running, " +
					"and works only when the instance's network is on an MTN Router with an external network. Changed in " +
					"place: the old address is released and a new one attached. Apply waits until the instance shows the " +
					"address (in `public_ip`), and fails if it doesn't.",
			},
			"expire_days": schema.Int64Attribute{
				Optional: true,
				Description: "Delete the instance this many days after it is created. Set at create only: afterwards extend " +
					"or cancel the expiry in the console (a change here is refused at plan time).",
				Validators: []validator.Int64{int64validator.AtLeast(1)},
			},
			"shutdown_days": schema.Int64Attribute{
				Optional:    true,
				Description: "Stop the VM this many days after it is created. Set at create only, like `expire_days`.",
				Validators:  []validator.Int64{int64validator.AtLeast(1)},
			},
			"expire_date": schema.StringAttribute{Computed: true, PlanModifiers: keep,
				Description: "When the instance will be deleted, if it expires (from `expire_days` or a policy)."},
			"shutdown_date": schema.StringAttribute{Computed: true, PlanModifiers: keep,
				Description: "When the VM will be stopped, if a shutdown is set."},
			"power_state": schema.StringAttribute{
				// Optional+Computed: left out, the VM's power is not managed and this shows what it is.
				Optional: true, Computed: true,
				Description: "`running`, `stopped` or `suspended`. Set it to power the VM on, off, or suspend it, in place: " +
					"stopping shuts the server down and keeps its disks and addresses. When set, a VM started or stopped in " +
					"the console is put back by the next apply. Left out, the VM's power isn't managed and this shows its " +
					"state. A plan change or added volume restarts the VM, then it is put back in this state.",
				Validators:    []validator.String{stringvalidator.OneOf(powerStates...)},
				PlanModifiers: keep,
			},
			"restart_trigger": schema.StringAttribute{
				Optional: true,
				Description: "Any value; changing it to a new value restarts the VM (server reboot) in place on the next " +
					"apply, for example `restart_trigger = timestamp()` or a version string. Nothing happens at create, " +
					"when it is removed, or while `power_state` is `stopped` or `suspended`.",
			},
			"status":     schema.StringAttribute{Computed: true, Description: "Status, for example `running`.", PlanModifiers: keep},
			"private_ip": schema.StringAttribute{Computed: true, Description: "Private IP address on the primary network.", PlanModifiers: keep},
			"public_ip":  schema.StringAttribute{Computed: true, Description: "Floating (public) IP address, when the instance has one.", PlanModifiers: keep},
			"public_ips": schema.ListAttribute{Computed: true, ElementType: types.StringType,
				PlanModifiers: []planmodifier.List{listplanmodifier.UseStateForUnknown()},
				Description:   publicIPsDescription},
		},
		Blocks: map[string]schema.Block{
			"data_volume": schema.ListNestedBlock{
				Description: "Extra disks, in order. Volumes added at the end are attached in place; changing or removing one " +
					"replaces the instance.",
				NestedObject: schema.NestedBlockObject{Attributes: map[string]schema.Attribute{
					// Optional+Computed: MTN Cloud names unnamed volumes.
					"name": schema.StringAttribute{Optional: true, Computed: true, Description: "Volume name.",
						PlanModifiers: keep},
					"size": schema.Int64Attribute{Required: true, Description: "Size in GB.",
						Validators: []validator.Int64{int64validator.AtLeast(1)}},
					"datastore": schema.StringAttribute{Optional: true, Description: "Datastore, by name. Left out, the project's default."},
				}},
			},
			"timeouts": timeouts.Block(ctx, timeouts.Opts{Create: true, Update: true, Delete: true}),
		},
	}
}

func usageRange(what string) schema.SingleNestedAttribute {
	pct := []validator.Float64{float64validator.Between(0, 100)}
	return schema.SingleNestedAttribute{
		Optional:    true,
		Description: what + " usage to watch, in percent. Left out, " + strings.ToLower(what) + " isn't watched.",
		Attributes: map[string]schema.Attribute{
			"min": schema.Float64Attribute{Required: true, Description: "Remove a VM below this.", Validators: pct},
			"max": schema.Float64Attribute{Required: true, Description: "Add a VM above this.", Validators: pct},
		},
	}
}

// ModifyPlan checks names at plan time (rule 17) and decides what a change replaces. The API can't
// change the primary network, an existing disk or the root size; changing a reference that names
// the same thing ("web-prod" vs "web-prod-Acme", an image's name vs its code) is no change at all.
func (r *instanceResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.Plan.Raw.IsNull() || r.client == nil {
		return // destroy, or validation before the provider is configured
	}
	if r.fillDefaults(ctx, &req, resp, "group", "project"); resp.Diagnostics.HasError() {
		return
	}
	var plan instanceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	checkScale(ctx, plan.Scale, &resp.Diagnostics)
	var state instanceModel
	hasState := !req.State.Raw.IsNull()
	if hasState {
		resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
		if resp.Diagnostics.HasError() {
			return
		}
		r.planChanges(ctx, plan, state, resp)
		defer r.planComputed(ctx, req, resp, state)
	}
	if !allKnown(plan.Group, plan.Project, plan.Image) {
		// Known only at apply: on an existing instance, assume a different one, which replaces it.
		for at, v := range map[string]types.String{"group": plan.Group, "project": plan.Project, "image": plan.Image} {
			if hasState && v.IsUnknown() {
				resp.RequiresReplace.Append(path.Root(at))
			}
		}
		return // names are checked at apply
	}
	p, err := r.client.PlacementForGroup(ctx, plan.Group.ValueString())
	if err != nil {
		resp.Diagnostics.AddAttributeError(path.Root("group"), "Unknown group", err.Error())
		return
	}
	project, err := r.client.ProjectByNameAt(ctx, p, plan.Project.ValueString())
	if err != nil {
		resp.Diagnostics.AddAttributeError(path.Root("project"), client.ProjectErrorTitle(err), err.Error())
		return
	}
	image, err := r.client.ImageByName(ctx, plan.Image.ValueString())
	if err != nil {
		resp.Diagnostics.AddAttributeError(path.Root("image"), "Unknown image", err.Error())
		return
	}
	r.checkNames(ctx, plan, p, project.ID, image, &resp.Diagnostics)
	r.checkNetworks(ctx, plan, state, hasState, project.ID, &resp.Diagnostics)
	// Only for a new attach: an address already attached isn't checked again (one router list per change).
	if !resp.Diagnostics.HasError() && known(plan.FloatingIPPool) && (!hasState || !sameName(plan.FloatingIPPool, state.FloatingIPPool)) {
		r.checkPoolRouter(ctx, plan, &resp.Diagnostics)
	}
	// On create only: an existing (or imported) instance already has its root volume.
	var root types.Int64
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("root_volume_size"), &root)...)
	if !hasState && isWindows(image) && !root.IsUnknown() && (root.IsNull() || root.ValueInt64() < windowsMinRootGB) {
		resp.Diagnostics.AddAttributeError(path.Root("root_volume_size"), "Root volume too small for Windows",
			fmt.Sprintf("%s needs a root volume of at least %d GB (MTN's VM Provisioning Workflow). Set root_volume_size = %d or more.",
				image.Name, windowsMinRootGB, windowsMinRootGB))
	}
	if !hasState || resp.Diagnostics.HasError() {
		return
	}
	// The same thing named another way (case, a code for a name, a short project name) is no change:
	// the state's spelling stays. A real change of these replaces the instance.
	same := func(at string, equal bool, stateValue types.String, replace bool) {
		switch {
		case equal:
			resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root(at), stateValue)...)
		case replace:
			resp.RequiresReplace.Append(path.Root(at))
		}
	}
	same("group", strings.EqualFold(plan.Group.ValueString(), state.Group.ValueString()), state.Group, true)
	from, err := r.client.ProjectByNameAt(ctx, p, state.Project.ValueString())
	same("project", err == nil && from.ID == project.ID, state.Project, true)
	was, err := r.client.ImageByName(ctx, state.Image.ValueString())
	same("image", err == nil && was.ID == image.ID, state.Image, true)
	if known(plan.Plan) {
		same("plan", strings.EqualFold(plan.Plan.ValueString(), state.Plan.ValueString()), state.Plan, false)
	}
	if !plan.Datastore.IsUnknown() {
		same("datastore", sameName(plan.Datastore, state.Datastore), state.Datastore, true)
	}
	if known(plan.Environment) && known(state.Environment) {
		a, errA := r.client.EnvironmentByName(ctx, plan.Environment.ValueString())
		b, errB := r.client.EnvironmentByName(ctx, state.Environment.ValueString())
		same("environment", errA == nil && errB == nil && a.Value == b.Value, state.Environment, false)
	}
	if !plan.Domain.IsUnknown() {
		equal := sameName(plan.Domain, state.Domain)
		if !equal && known(plan.Domain) && known(state.Domain) {
			a, errA := r.client.NetworkDomainByName(ctx, plan.Domain.ValueString())
			b, errB := r.client.NetworkDomainByName(ctx, state.Domain.ValueString())
			equal = errA == nil && errB == nil && a.ID == b.ID
		}
		same("domain", equal, state.Domain, true)
	}
	if !plan.FloatingIPPool.IsUnknown() {
		same("floating_ip_pool", sameName(plan.FloatingIPPool, state.FloatingIPPool), state.FloatingIPPool, false)
	}
	planVols, stateVols := volumesOf(ctx, plan.DataVolumes, &resp.Diagnostics), volumesOf(ctx, state.DataVolumes, &resp.Diagnostics)
	for i := 0; i < len(planVols) && i < len(stateVols); i++ {
		if !planVols[i].Datastore.Equal(stateVols[i].Datastore) && sameName(planVols[i].Datastore, stateVols[i].Datastore) {
			at := path.Root("data_volume").AtListIndex(i).AtName("datastore")
			resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, at, stateVols[i].Datastore)...)
		}
	}
}

// planChanges decides what the API can't change in place: the root size, existing networks and
// volumes. It needs no names resolved, so it also runs when a reference is unknown until apply.
func (r *instanceResource) planChanges(ctx context.Context, plan, state instanceModel, resp *resource.ModifyPlanResponse) {
	if v := plan.RootVolumeSize; !v.IsUnknown() && !v.IsNull() && v.ValueInt64() != state.RootVolumeSize.ValueInt64() {
		resp.Diagnostics.AddAttributeError(path.Root("root_volume_size"), "Root volume can't be resized",
			fmt.Sprintf("MTN Cloud can't change the root volume (%d GB) of an existing instance. Add a data_volume instead, "+
				"or replace the instance with `terraform apply -replace=<address>`.", state.RootVolumeSize.ValueInt64()))
	}
	// Any change: MTN Cloud reports an instance's cards without IDs, and a resize that lists a card
	// without its ID adds it again, so a network can't be added in place (seen live, 2026-10-28).
	if !plan.Networks.Equal(state.Networks) {
		resp.RequiresReplace.Append(path.Root("networks"))
	}
	if !r.volumesAppendOnly(ctx, plan, state, &resp.Diagnostics) {
		resp.RequiresReplace.Append(path.Root("data_volume"))
	}
	// The framework leaves the name of an added volume null, but MTN Cloud names it: unknown.
	vols := volumesOf(ctx, plan.DataVolumes, &resp.Diagnostics)
	for i := len(volumesOf(ctx, state.DataVolumes, &resp.Diagnostics)); i < len(vols); i++ {
		if vols[i].Name.IsNull() {
			at := path.Root("data_volume").AtListIndex(i).AtName("name")
			resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, at, types.StringUnknown())...)
		}
	}
	// A new hostname replaces the instance (RequiresReplaceIfConfigured); a removed one keeps it.
	if plan.Hostname.IsNull() {
		resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("hostname"), state.Hostname)...)
	}
	// A failed or errored instance is replaced: nothing the provider can send repairs it.
	if client.InstanceFailed(state.Status.ValueString()) {
		resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("status"), types.StringUnknown())...)
		resp.RequiresReplace.Append(path.Root("status"))
	}
}

// planComputed marks the values an update can change as unknown: a reconfigure restarts the VM
// (its status may change, for example from "stopped" to "running") and a floating IP may move.
func (r *instanceResource) planComputed(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse, state instanceModel) {
	if len(resp.RequiresReplace) == 0 {
		var plan instanceModel
		resp.Diagnostics.Append(resp.Plan.Get(ctx, &plan)...)
		checkLifecycle(plan, state, &resp.Diagnostics)
	}
	if resp.Diagnostics.HasError() || resp.Plan.Raw.Equal(req.State.Raw) {
		return
	}
	resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("status"), types.StringUnknown())...)
	resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("public_ip"), types.StringUnknown())...)
	resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("public_ips"), types.ListUnknown(types.StringType))...)
	// Unmanaged power can change with the update (a reconfigure starts a stopped VM).
	var power types.String
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("power_state"), &power)...)
	if power.IsNull() {
		resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("power_state"), types.StringUnknown())...)
	}
}

// checkLifecycle refuses a change to the create-only days of an instance that is kept (a replaced
// one gets the new days). Days are null in state after an import: setting them then can't be checked
// against the platform, so it only warns that nothing changes.
func checkLifecycle(plan, state instanceModel, diags *diag.Diagnostics) {
	for _, f := range []struct {
		at         string
		plan, from types.Int64
	}{{"expire_days", plan.ExpireDays, state.ExpireDays}, {"shutdown_days", plan.ShutdownDays, state.ShutdownDays}} {
		switch {
		case f.plan.IsUnknown() || f.plan.Equal(f.from):
		case f.from.IsNull():
			diags.AddAttributeWarning(path.Root(f.at), "Set at create only",
				fmt.Sprintf("%s only applies when an instance is created; this one keeps the expiry or shutdown it has "+
					"(see expire_date and shutdown_date).", f.at))
		default:
			undo := fmt.Sprintf("set %s back to %d", f.at, f.from.ValueInt64())
			if f.plan.IsNull() {
				undo = fmt.Sprintf("put %s = %d back", f.at, f.from.ValueInt64())
			}
			diags.AddAttributeError(path.Root(f.at), "Set at create only",
				fmt.Sprintf("MTN Cloud sets %s when the instance is created; it can't be changed afterwards. Extend or cancel "+
					"the expiry or shutdown in the console, and %s.", f.at, undo))
		}
	}
}

// checkNames resolves every name the plan uses, so a typo fails at plan time with the valid names.
func (r *instanceResource) checkNames(ctx context.Context, m instanceModel, p *client.Placement, projectID int64, image client.InstanceType, diags *diag.Diagnostics) {
	if v := m.Plan; !v.IsUnknown() {
		if _, err := r.client.PlanByName(ctx, p, projectID, image.LayoutID, v.ValueString()); err != nil {
			diags.AddAttributeError(path.Root("plan"), "Unknown plan", err.Error())
		}
	}
	for _, name := range stringsOf(ctx, m.SecurityGroups, diags) {
		if _, err := r.client.SecurityGroupByName(ctx, p, projectID, name); err != nil {
			diags.AddAttributeError(path.Root("security_groups"), "Unknown security group", err.Error())
		}
	}
	stores := []types.String{m.Datastore}
	for _, v := range volumesOf(ctx, m.DataVolumes, diags) {
		stores = append(stores, v.Datastore)
	}
	for _, s := range stores {
		if known(s) {
			if _, err := r.client.DatastoreByName(ctx, p, projectID, s.ValueString()); err != nil {
				diags.AddAttributeError(path.Root("datastore"), "Unknown datastore", err.Error())
			}
		}
	}
	if known(m.Environment) {
		if _, err := r.client.EnvironmentByName(ctx, m.Environment.ValueString()); err != nil {
			diags.AddAttributeError(path.Root("environment"), "Unknown environment", err.Error())
		}
	}
	if known(m.Domain) {
		if _, err := r.client.NetworkDomainByName(ctx, m.Domain.ValueString()); err != nil {
			diags.AddAttributeError(path.Root("domain"), "Unknown network domain", err.Error())
		}
	}
	if known(m.FloatingIPPool) {
		// The pools carry the external networks' names (captured 2026-10-27); their own list needs a
		// VM, so a new instance is checked by name here and resolved after it runs.
		nets, err := r.client.ExternalNetworks(ctx)
		if err != nil {
			diags.AddError("Unable to list the floating IP pools", err.Error())
			return
		}
		names := make([]string, len(nets))
		for i, n := range nets {
			if strings.EqualFold(n.Name, m.FloatingIPPool.ValueString()) {
				return
			}
			names[i] = n.Name
		}
		diags.AddAttributeError(path.Root("floating_ip_pool"), "Unknown floating IP pool",
			fmt.Sprintf("no floating IP pool named %q. Available pools: %s. %s", m.FloatingIPPool.ValueString(), strings.Join(names, ", "), client.SyncHint))
	}
}

// checkNetworks checks that each network ID known at plan time is a network in the instance's
// project, so a wrong ID fails the plan instead of the create. IDs not known yet (a network made in
// the same apply) and IDs the instance already has are skipped.
func (r *instanceResource) checkNetworks(ctx context.Context, plan, state instanceModel, hasState bool, projectID int64, diags *diag.Diagnostics) {
	if plan.Networks.IsUnknown() || plan.Networks.IsNull() {
		return
	}
	had := map[string]bool{}
	if hasState && sameName(plan.Project, state.Project) { // a project move makes a new VM: check them all

		for _, s := range stringsOf(ctx, state.Networks, diags) {
			had[s] = true
		}
	}
	for _, e := range plan.Networks.Elements() {
		s, ok := e.(types.String)
		if !ok || !known(s) || had[s.ValueString()] {
			continue
		}
		id, err := strconv.ParseInt(s.ValueString(), 10, 64)
		if err != nil {
			continue // the attribute's validator reports it
		}
		n, err := r.client.GetNetwork(ctx, id)
		switch {
		case client.IsNotFound(err):
			diags.AddAttributeError(path.Root("networks"), "Unknown network",
				fmt.Sprintf("No network with ID %d exists. Create the network first, or use its resource's id, "+
					"for example mtncloud_network.app.id.", id))
		case err != nil:
			diags.AddAttributeError(path.Root("networks"), "Unable to read network", err.Error())
		case n.ProjectID != 0 && n.ProjectID != projectID:
			diags.AddAttributeError(path.Root("networks"), "Network in another project",
				fmt.Sprintf("Network %d (%s) is in project %q, not the instance's project. Use a network in the same project.", id, n.Name, n.ProjectName))
		}
	}
}

// checkPoolRouter checks, before anything is created, that a floating IP can be attached from the
// pool: the primary network must be on an MTN Router whose external network is that pool. A router
// on another external network is an error. No router is only a warning, as the router may be made
// in the same apply; a network not known yet (made in the same apply) isn't checked.
func (r *instanceResource) checkPoolRouter(ctx context.Context, m instanceModel, diags *diag.Diagnostics) {
	if m.Networks.IsUnknown() || m.Networks.IsNull() || len(m.Networks.Elements()) == 0 {
		return
	}
	first, ok := m.Networks.Elements()[0].(types.String)
	if !ok || !known(first) {
		return
	}
	netID, err := strconv.ParseInt(first.ValueString(), 10, 64)
	if err != nil {
		return // reported by the network check
	}
	msg, isErr := r.client.PoolRouterProblem(ctx, netID, m.FloatingIPPool.ValueString())
	switch {
	case msg == "":
	case isErr:
		diags.AddAttributeError(path.Root("floating_ip_pool"), "Floating IP pool doesn't match the router", msg)
	default:
		diags.AddAttributeWarning(path.Root("floating_ip_pool"), "Floating IP pool not checked against a router", msg)
	}
}

// volumesAppendOnly reports whether existing data volumes are unchanged (new ones may follow). An
// unset name or datastore means "whatever it is now".
func (r *instanceResource) volumesAppendOnly(ctx context.Context, plan, state instanceModel, diags *diag.Diagnostics) bool {
	if plan.DataVolumes.IsUnknown() {
		return false
	}
	p, s := volumesOf(ctx, plan.DataVolumes, diags), volumesOf(ctx, state.DataVolumes, diags)
	if len(p) < len(s) {
		return false
	}
	for i := range s {
		if !p[i].Size.Equal(s[i].Size) ||
			(known(p[i].Name) && p[i].Name.ValueString() != s[i].Name.ValueString()) ||
			!sameName(p[i].Datastore, s[i].Datastore) {
			return false
		}
	}
	return true
}

func isWindows(t client.InstanceType) bool {
	return strings.Contains(strings.ToLower(t.Name+" "+t.Code), "win")
}

// resolved is the planned instance in API terms.
type resolved struct {
	placement *client.Placement
	input     client.InstanceInput
}

// input resolves the planned instance into API IDs. The project's default security group is always
// added (MTN's VM Provisioning Workflow).
func (r *instanceResource) input(ctx context.Context, m instanceModel, diags *diag.Diagnostics) (resolved, bool) {
	var out resolved
	p, err := r.client.PlacementForGroup(ctx, m.Group.ValueString())
	if err != nil {
		diags.AddAttributeError(path.Root("group"), "Unknown group", err.Error())
		return out, false
	}
	out.placement = p
	project, err := r.client.ProjectByNameAt(ctx, p, m.Project.ValueString())
	if err != nil {
		diags.AddAttributeError(path.Root("project"), client.ProjectErrorTitle(err), err.Error())
		return out, false
	}
	image, err := r.client.ImageByName(ctx, m.Image.ValueString())
	if err != nil {
		diags.AddAttributeError(path.Root("image"), "Unknown image", err.Error())
		return out, false
	}
	plan, err := r.client.PlanByName(ctx, p, project.ID, image.LayoutID, m.Plan.ValueString())
	if err != nil {
		diags.AddAttributeError(path.Root("plan"), "Unknown plan", err.Error())
		return out, false
	}
	in := client.InstanceInput{
		Name: m.Name.ValueString(), Description: m.Description.ValueString(), HostName: m.Hostname.ValueString(),
		Placement: *p, ProjectID: project.ID, ImageCode: image.Code, LayoutID: image.LayoutID, PlanID: plan.ID,
		Labels: stringsOf(ctx, m.Labels, diags), Tags: mapOf(ctx, m.Tags, diags), EnvVars: mapOf(ctx, m.EnvVars, diags),
		NoAgent: !m.InstallAgent.ValueBool(), CreateUser: m.CreateUser.ValueBool(), LayoutSize: m.ScaleFactor.ValueInt64(),
		ExpireDays: m.ExpireDays.ValueInt64(), ShutdownDays: m.ShutdownDays.ValueInt64(),
	}
	if !known(m.Name) {
		in.Name = ""
	}
	if !known(m.Hostname) {
		in.HostName = ""
	}
	slices.Sort(in.Labels)
	if known(m.Environment) {
		env, err := r.client.EnvironmentByName(ctx, m.Environment.ValueString())
		if err != nil {
			diags.AddAttributeError(path.Root("environment"), "Unknown environment", err.Error())
			return out, false
		}
		in.Environment = env.Value
	}
	if known(m.Domain) {
		d, err := r.client.NetworkDomainByName(ctx, m.Domain.ValueString())
		if err != nil {
			diags.AddAttributeError(path.Root("domain"), "Unknown network domain", err.Error())
			return out, false
		}
		in.NetworkDomainID = d.ID
	}
	sgs, err := r.client.SecurityGroupIDs(ctx, p, project.ID, stringsOf(ctx, m.SecurityGroups, diags))
	if err != nil {
		diags.AddAttributeError(path.Root("security_groups"), "Unknown security group", err.Error())
		return out, false
	}
	in.SecurityGroupIDs = sgs
	var ok bool
	root := client.VolumeSpec{Root: true, Name: "root", Size: plan.RootBytes >> 30}
	if v := m.RootVolumeSize; !v.IsNull() && !v.IsUnknown() {
		root.Size = v.ValueInt64()
	}
	if root.DatastoreID, ok = r.datastoreID(ctx, p, project.ID, m.Datastore, diags); !ok {
		return out, false
	}
	in.Volumes = []client.VolumeSpec{root}
	for _, v := range volumesOf(ctx, m.DataVolumes, diags) {
		spec := client.VolumeSpec{Size: v.Size.ValueInt64()}
		if known(v.Name) {
			spec.Name = v.Name.ValueString()
		}
		if spec.DatastoreID, ok = r.datastoreID(ctx, p, project.ID, v.Datastore, diags); !ok {
			return out, false
		}
		in.Volumes = append(in.Volumes, spec)
	}
	for _, s := range stringsOf(ctx, m.Networks, diags) {
		id, _ := strconv.ParseInt(s, 10, 64)
		in.NetworkIDs = append(in.NetworkIDs, id)
	}
	out.input = in
	return out, !diags.HasError()
}

// datastoreID is "auto" (the project's default) for an unset datastore, or the named one's ID.
func (r *instanceResource) datastoreID(ctx context.Context, p *client.Placement, projectID int64, name types.String, diags *diag.Diagnostics) (string, bool) {
	if !known(name) {
		return "auto", true
	}
	ds, err := r.client.DatastoreByName(ctx, p, projectID, name.ValueString())
	if err != nil {
		diags.AddAttributeError(path.Root("datastore"), "Unknown datastore", err.Error())
		return "", false
	}
	return strconv.FormatInt(ds.ID, 10), true
}

func (r *instanceResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	if !r.ready(&resp.Diagnostics) {
		return
	}
	var plan instanceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	timeout, d := plan.Timeouts.Create(ctx, instanceTimeout)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	// Decided here, not at plan: a router made in the same apply may attach the network after the
	// plan, and a value fixed at plan would then differ from the one planned again during apply.
	if plan.InstallAgent.IsUnknown() {
		on, _ := r.agentByRouter(ctx, plan, &resp.Diagnostics)
		plan.InstallAgent = types.BoolValue(on)
	}
	res, ok := r.input(ctx, plan, &resp.Diagnostics)
	if !ok {
		return
	}
	in := res.input

	// An instance of this name in the project is never adopted: it may not be ours.
	if in.Name != "" {
		existing, err := r.client.FindInstances(ctx, in.Name, in.ProjectID)
		if err != nil {
			resp.Diagnostics.AddError("Unable to check for an existing instance", err.Error())
			return
		}
		if len(existing) > 0 {
			resp.Diagnostics.AddAttributeError(path.Root("name"), "Instance already exists",
				fmt.Sprintf("Project %q already has an instance named %q (ID %d). Import it with `terraform import <address> %d`, or choose another name.",
					plan.Project.ValueString(), in.Name, existing[0].ID, existing[0].ID))
			return
		}
	}
	id, err := r.client.CreateInstance(ctx, in)
	if err != nil {
		// The request may have been applied (rule 11): the reply was lost, or the timeout ended the
		// wait for it. With a name, one now in the project is ours; the lookup gets its own short
		// deadline, because ctx may be the one that just ran out. Without a name (the naming template
		// picks it) there is nothing safe to look for.
		if (!client.MayHaveSucceeded(err) && ctx.Err() == nil) || in.Name == "" {
			if client.MayHaveSucceeded(err) || ctx.Err() != nil {
				err = fmt.Errorf("%w. MTN Cloud may have created the instance anyway: check the project in the console and "+
					"import it, or delete it before applying again", err)
			}
			resp.Diagnostics.AddError("Unable to create instance", err.Error())
			return
		}
		lookup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		found, ferr := r.client.FindInstances(lookup, in.Name, in.ProjectID)
		cancel()
		if len(found) == 0 {
			if ferr != nil {
				err = fmt.Errorf("%w; looking for the instance afterwards also failed: %w", err, ferr)
			}
			resp.Diagnostics.AddError("Unable to create instance", err.Error())
			return
		}
		id = found[0].ID
	}
	// From here the instance exists: it goes into state whatever happens, so a failed or stuck one
	// is replaced by the next apply instead of being orphaned.
	plan.ID = types.StringValue(strconv.FormatInt(id, 10))
	inst, err := r.client.WaitForInstance(ctx, id)
	planned, scaleFailed := plan.Scale, false
	if err == nil && !client.InstanceFailed(inst.Status) && !plan.Scale.IsNull() {
		if serr := r.client.SetInstanceThreshold(ctx, id, thresholdOf(ctx, plan.Scale, &resp.Diagnostics)); serr != nil {
			// A warning, not an error: an error would taint (and so replace) a healthy VM. Read finds
			// no scaling, so the next plan sets it in place.
			resp.Diagnostics.AddAttributeWarning(path.Root("scale"), "Scaling not set yet",
				fmt.Sprintf("Instance %d was created, but its scaling wasn't set: %s. The next apply sets it.", id, serr))
			scaleFailed = true
		}
	}
	fipFailed := false
	if err == nil && !client.InstanceFailed(inst.Status) && known(plan.FloatingIPPool) {
		if got, done, ferr := r.setFloatingIP(ctx, inst, types.StringNull(), plan.FloatingIPPool); ferr != nil {
			// The apply that asked for it fails (owner, 2026-10-27). The VM is kept in state, so Terraform
			// marks it tainted: `terraform untaint` keeps it, and the next apply then attaches again.
			resp.Diagnostics.AddAttributeError(path.Root("floating_ip_pool"), "Floating IP not attached",
				fmt.Sprintf("Instance %d was created, but its floating IP wasn't attached: %s. Terraform marks the instance "+
					"tainted, so the next apply replaces it; run terraform untaint to keep it and try the attach again.", id, ferr))
			fipFailed = !done.unconfirmed
			if done.unconfirmed {
				recordUnconfirmed(ctx, resp.Private, &resp.Diagnostics)
			}
		} else {
			inst = got
		}
	}
	plannedPower, powerFailed := plan.PowerState, false
	if err == nil && !client.InstanceFailed(inst.Status) && !fipFailed {
		// The VM is up: a power change that doesn't finish is a warning, so it isn't replaced.
		got, perr := r.power(ctx, &plan, inst, false, true, &resp.Diagnostics)
		if perr != nil {
			resp.Diagnostics.AddAttributeWarning(path.Root("power_state"), "Power state not reached yet",
				fmt.Sprintf("Instance %d was created, but hasn't reached %s: %s. The next apply checks again.", id, plan.PowerState.ValueString(), perr))
		}
		if got != nil {
			inst = got
		}
		powerFailed = perr != nil || plan.PowerState.IsNull() != plannedPower.IsNull()
	}
	r.settle(ctx, &plan, inst, err, "create", &resp.Diagnostics)
	if scaleFailed {
		plan.Scale = planned // as planned, so the apply is consistent; the next refresh shows it unset
	}
	if fipFailed {
		plan.FloatingIPPool = types.StringNull() // not attached
	}
	if powerFailed {
		plan.PowerState = plannedPower // as planned, so the apply is consistent; the next refresh shows it
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// powerStates are the values of power_state, each the instance status it ends in.
var powerStates = []string{"running", "stopped", "suspended"}

// powerActions turns a wanted power_state into the call that reaches it.
var powerActions = map[string]string{"running": client.PowerStart, "stopped": client.PowerStop, "suspended": client.PowerSuspend}

// power puts the VM in m's power_state (unknown or null: left as it is), restarting it instead when
// restart is set and it is to run. A VM whose power is changing (client.PowerOf is "") is left
// alone. A refused call is reported on power_state, and m then records the VM's power as it is, so
// the next plan tries again. At create it is a warning, not an error: an error would taint, and so
// replace, the new VM. The wait's own error goes back to the caller.
func (r *instanceResource) power(ctx context.Context, m *instanceModel, inst *client.Instance, restart, create bool, diags *diag.Diagnostics) (*client.Instance, error) {
	have := client.PowerOf(inst.Status)
	want := have
	if known(m.PowerState) {
		want = m.PowerState.ValueString()
	}
	action := powerActions[want]
	switch {
	case have == "":
		return inst, nil
	case restart && want == "running" && have == "running":
		action = client.PowerRestart
	case have == want:
		return inst, nil
	}
	if err := r.client.PowerInstance(ctx, inst.ID, action); err != nil {
		report := diags.AddAttributeError
		if create {
			report = diags.AddAttributeWarning
		}
		report(path.Root("power_state"), "Unable to "+action+" the instance",
			fmt.Sprintf("Instance %d is %q: %s. The next apply tries again.", inst.ID, inst.Status, err))
		m.PowerState = types.StringNull() // read back from the VM by refresh
		return inst, nil
	}
	if action == client.PowerRestart {
		return r.client.WaitForRestart(ctx, inst.ID)
	}
	got, err := r.client.WaitForPowerChange(ctx, inst.ID, have, want)
	if got == nil {
		got = inst
	}
	if errors.Is(err, client.ErrPowerIgnored) {
		report := diags.AddAttributeError
		if create {
			report = diags.AddAttributeWarning
		}
		report(path.Root("power_state"), "Instance didn't "+action,
			fmt.Sprintf("MTN Cloud accepted the %s, but instance %d is still %q after the grace period (2 minutes); this VM may not support "+
				"it (suspend isn't available on every VM). Use \"stopped\" instead, or check it in the console.", action, inst.ID, have))
		m.PowerState = types.StringNull() // read back from the VM by refresh
		return got, nil
	}
	return got, err
}

// settle records the instance after a create or update wait and reports a failure or a timeout.
func (r *instanceResource) settle(ctx context.Context, m *instanceModel, inst *client.Instance, err error, op string, diags *diag.Diagnostics) {
	switch {
	case err != nil && errors.Is(err, context.DeadlineExceeded):
		status := "unknown"
		if inst != nil {
			status = inst.Status
		}
		diags.AddError("Timed out waiting for the instance",
			fmt.Sprintf("Instance %s is still %q after the %s timeout. Check it in the console, or raise `timeouts { %s = \"60m\" }`.",
				m.ID.ValueString(), status, op, op))
	case err != nil:
		diags.AddError("Unable to read instance", err.Error())
	case client.InstanceFailed(inst.Status):
		msg := inst.StatusMessage
		if msg == "" {
			msg = "no reason given"
		}
		diags.AddError("Instance "+op+" failed",
			fmt.Sprintf("MTN Cloud reports instance %d as %q: %s. The next apply replaces it.", inst.ID, inst.Status, msg))
	}
	if inst != nil && inst.ID != 0 {
		read, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
		defer cancel()
		r.refresh(read, m, inst, diags)
	}
	unknownToNull(ctx, m)
}

// unknownToNull clears computed values that were never read, so state can be saved.
func unknownToNull(ctx context.Context, m *instanceModel) {
	for _, s := range []*types.String{&m.Name, &m.Hostname, &m.Domain, &m.FloatingIPPool, &m.PowerState, &m.Status, &m.PrivateIP, &m.PublicIP, &m.ExpireDate, &m.ShutdownDate} {
		if s.IsUnknown() {
			*s = types.StringNull()
		}
	}
	if m.RootVolumeSize.IsUnknown() {
		m.RootVolumeSize = types.Int64Null()
	}
	if m.PublicIPs.IsUnknown() {
		m.PublicIPs = types.ListNull(types.StringType)
	}
	if m.DataVolumes.IsUnknown() {
		m.DataVolumes = types.ListNull(dataVolumeType)
		return
	}
	vols := volumesOf(ctx, m.DataVolumes, nil)
	for i := range vols {
		if vols[i].Name.IsUnknown() {
			vols[i].Name = types.StringNull()
		}
	}
	if len(vols) > 0 {
		m.DataVolumes, _ = types.ListValueFrom(ctx, dataVolumeType, vols)
	}
}

func (r *instanceResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	if !r.ready(&resp.Diagnostics) {
		return
	}
	var state instanceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	id, err := strconv.ParseInt(state.ID.ValueString(), 10, 64)
	if err != nil {
		resp.Diagnostics.AddError("Invalid instance ID", fmt.Sprintf("%q is not an instance ID: %s", state.ID.ValueString(), err))
		return
	}
	inst, err := r.client.GetInstance(ctx, id)
	if client.IsNotFound(err) || (err == nil && strings.EqualFold(inst.Status, "pendingRemoval")) {
		resp.State.RemoveResource(ctx) // deleted outside Terraform: plan a new one
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Unable to read instance", err.Error())
		return
	}
	r.refresh(ctx, &state, inst, &resp.Diagnostics)
	// Every attach waits for its address, so a missing one was released outside Terraform and the next
	// apply attaches one again; except an attach that was sent but never seen, which may yet arrive.
	pending, d := req.Private.GetKey(ctx, fipUnconfirmedKey)
	resp.Diagnostics.Append(d...)
	switch {
	case !inst.MissingPublicIP() && pending != nil:
		resp.Diagnostics.Append(resp.Private.SetKey(ctx, fipUnconfirmedKey, nil)...)
	case inst.MissingPublicIP() && pending != nil && !state.FloatingIPPool.IsNull():
		resp.Diagnostics.AddAttributeWarning(path.Root("floating_ip_pool"), "Floating IP not attached yet",
			fmt.Sprintf("A floating IP from %s was asked for but hasn't shown on instance %s. It isn't asked for again, "+
				"so no second address is attached. If it never appears, remove floating_ip_pool, apply, then add it back.",
				state.FloatingIPPool.ValueString(), state.ID.ValueString()))
	case inst.MissingPublicIP() && !state.FloatingIPPool.IsUnknown():
		// With several servers, one that lost its address is enough: the next apply attaches it one.
		// The pool the others are on is kept aside, so a pool change in that apply moves them too.
		if !state.FloatingIPPool.IsNull() && !inst.NoPublicIP() {
			rememberPartial(ctx, resp.Private, state.FloatingIPPool, &resp.Diagnostics)
		}
		state.FloatingIPPool = types.StringNull()
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *instanceResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	if !r.ready(&resp.Diagnostics) {
		return
	}
	var plan, state instanceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	timeout, d := plan.Timeouts.Update(ctx, instanceTimeout)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	res, ok := r.input(ctx, plan, &resp.Diagnostics)
	if !ok {
		return
	}
	in := res.input
	id, _ := strconv.ParseInt(state.ID.ValueString(), 10, 64)
	plan.ID = state.ID
	current, err := r.client.GetInstance(ctx, id)
	if err != nil {
		resp.Diagnostics.AddError("Unable to read instance", err.Error())
		return
	}

	if !plan.Name.Equal(state.Name) || !plan.Description.Equal(state.Description) || !plan.Environment.Equal(state.Environment) ||
		!plan.Labels.Equal(state.Labels) || !plan.Tags.Equal(state.Tags) {
		u := client.InstanceUpdate{Description: in.Description, Environment: in.Environment, Labels: in.Labels, Tags: in.Tags}
		if !plan.Name.Equal(state.Name) {
			u.Name = in.Name
		}
		if err := r.client.UpdateInstance(ctx, id, u); err != nil {
			resp.Diagnostics.AddError("Unable to update instance", err.Error())
			return
		}
	}
	if !plan.Scale.Equal(state.Scale) {
		if err := r.client.SetInstanceThreshold(ctx, id, thresholdOf(ctx, plan.Scale, &resp.Diagnostics)); err != nil {
			resp.Diagnostics.AddError("Unable to set the instance's scaling", err.Error())
			return
		}
	}
	if !plan.SecurityGroups.Equal(state.SecurityGroups) {
		if err := r.client.SetInstanceSecurityGroups(ctx, id, in.SecurityGroupIDs); err != nil {
			resp.Diagnostics.AddError("Unable to set the instance's security groups", err.Error())
			return
		}
	}
	resized := false
	if current.PlanID != in.PlanID || len(in.Volumes) > len(current.Volumes) {
		resize := client.ResizeInput{PlanID: in.PlanID}
		// Existing volumes and cards go with their IDs, new ones (at the end) without.
		for i, v := range in.Volumes {
			if i < len(current.Volumes) {
				cur := current.Volumes[i]
				v = client.VolumeSpec{ID: cur.ID, Root: cur.Root, Name: cur.Name, Size: cur.Size, DatastoreID: cur.DatastoreID}
			}
			resize.Volumes = append(resize.Volumes, v)
		}
		// No cards are sent: a card sent without its ID is added again, and networks never change in place.
		if err := r.client.ResizeInstance(ctx, id, resize); err != nil {
			resp.Diagnostics.AddError("Unable to reconfigure instance", err.Error())
			return
		}
		resized = true
	}
	var inst *client.Instance
	if resized {
		inst, err = r.client.WaitForResize(ctx, id, in.PlanID, len(in.Volumes), 0)
		if errors.Is(err, context.DeadlineExceeded) && inst != nil {
			// Accepted, but not applied (B-016): say where it was left. State takes the instance as
			// it is, so the next plan shows the resize again.
			resp.Diagnostics.AddError("Resize not applied",
				fmt.Sprintf("MTN Cloud accepted the resize of instance %d, but by the update timeout it was still on plan %q "+
					"(asked for %q), status %q, with %d of %d disks. Check it in the console, then apply again, or raise "+
					"`timeouts { update = \"60m\" }` if it is only slow.", id, inst.PlanName, plan.Plan.ValueString(), inst.Status,
					len(inst.Volumes), len(in.Volumes)))
			err = nil
		}
	} else {
		inst, err = r.client.WaitForInstance(ctx, id)
	}
	if !sameName(plan.FloatingIPPool, state.FloatingIPPool) {
		if err != nil {
			plan.FloatingIPPool = state.FloatingIPPool // not changed: the next apply tries again
		} else {
			got, done, ferr := r.setFloatingIP(ctx, inst, fromPool(ctx, req.Private, state.FloatingIPPool, plan.FloatingIPPool, &resp.Diagnostics), plan.FloatingIPPool)
			if ferr != nil {
				resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("public_ips"), types.ListValueMust(types.StringType, nil))...)
				resp.Diagnostics.AddAttributeError(path.Root("floating_ip_pool"), "Unable to change the floating IP", ferr.Error())
				switch {
				case done.unconfirmed:
					// Sent but not seen: record it as asked, so no second one is attached.
					resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("floating_ip_pool"), plan.FloatingIPPool)...)
					resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("public_ip"), types.StringNull())...)
					resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("timeouts"), plan.Timeouts)...)
					recordUnconfirmed(ctx, resp.Private, &resp.Diagnostics)
				case done.released:
					// The old address is confirmed gone: record none, so the next apply attaches one.
					resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("floating_ip_pool"), types.StringNull())...)
					resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("public_ip"), types.StringNull())...)
				}
				return
			}
			inst = got
			resp.Diagnostics.Append(resp.Private.SetKey(ctx, fipUnconfirmedKey, nil)...)
			resp.Diagnostics.Append(resp.Private.SetKey(ctx, fipPartialKey, nil)...)
		}
	}
	if err == nil && !client.InstanceFailed(inst.Status) {
		restart := !plan.RestartTrigger.IsNull() && !plan.RestartTrigger.Equal(state.RestartTrigger)
		inst, err = r.power(ctx, &plan, inst, restart, false, &resp.Diagnostics)
	}
	r.settle(ctx, &plan, inst, err, "update", &resp.Diagnostics)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *instanceResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	if !r.ready(&resp.Diagnostics) {
		return
	}
	var state instanceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	timeout, d := state.Timeouts.Delete(ctx, instanceTimeout)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	id, _ := strconv.ParseInt(state.ID.ValueString(), 10, 64)
	if err := r.client.DeleteInstance(ctx, id); err != nil {
		resp.Diagnostics.AddError("Unable to delete instance", err.Error())
		return
	}
	// Wait for it to go, so its networks and security groups can be deleted straight after.
	if err := r.client.WaitForInstanceGone(ctx, id); err != nil {
		resp.Diagnostics.AddError("Instance not deleted yet",
			fmt.Sprintf("Instance %d still exists after the delete (%s). Check it in the console, or raise `timeouts { delete = \"60m\" }`.", id, err))
	}
}

func (r *instanceResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	if _, err := strconv.ParseInt(req.ID, 10, 64); err != nil {
		resp.Diagnostics.AddError("Invalid import ID", fmt.Sprintf("Import an instance by its numeric ID, for example 9701. Got %q.", req.ID))
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
	// Not readable from the API; these are the defaults every console-made instance also uses.
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("create_user"), true)...)
}

// refresh copies the API's view of an instance into the model. Where the configuration names the
// same thing another way (case, an image's code, a project's short name, an environment's code),
// its spelling is kept, so there is no diff.
func (r *instanceResource) refresh(ctx context.Context, m *instanceModel, in *client.Instance, diags *diag.Diagnostics) {
	m.ID = types.StringValue(strconv.FormatInt(in.ID, 10))
	m.Name = types.StringValue(in.Name)
	m.Hostname = optional(in.HostName)
	if in.Description != "" || !m.Description.IsNull() {
		m.Description = types.StringValue(in.Description)
	}
	m.Status = types.StringValue(in.Status)
	if p := client.PowerOf(in.Status); p != "" {
		m.PowerState = types.StringValue(p)
	}
	m.PrivateIP = optional(in.PrivateIP)
	m.PublicIPs = serverIPs(ctx, r.client, in, diags) // may set in.PublicIP from a server
	m.PublicIP = optional(in.PublicIP)
	m.InstallAgent = types.BoolValue(!in.NoAgent)
	m.ScaleFactor = types.Int64Value(in.LayoutSize)
	// expire_days and shutdown_days stay as configured: a policy may set days the configuration
	// doesn't, and those show in the dates.
	m.ExpireDate, m.ShutdownDate = optional(in.ExpireDate), optional(in.ShutdownDate)
	if m.CreateUser.IsNull() || m.CreateUser.IsUnknown() {
		m.CreateUser = types.BoolValue(true)
	}

	if !strings.EqualFold(m.Group.ValueString(), in.GroupName) {
		m.Group = types.StringValue(in.GroupName)
	}
	if m.Image.ValueString() != in.ImageCode && !strings.EqualFold(m.Image.ValueString(), in.ImageName) {
		m.Image = types.StringValue(in.ImageName)
	}
	if !strings.EqualFold(m.Plan.ValueString(), in.PlanName) {
		m.Plan = types.StringValue(in.PlanName)
	}
	p, err := r.client.PlacementForGroup(ctx, m.Group.ValueString())
	if err != nil {
		diags.AddError("Unable to read the instance's group", err.Error())
		return
	}
	r.refreshProject(ctx, m, p, in.ProjectID, diags)

	nets := make([]string, len(in.Interfaces))
	for i, n := range in.Interfaces {
		nets[i] = strconv.FormatInt(n.NetworkID, 10)
	}
	m.Networks, _ = types.ListValueFrom(ctx, types.StringType, nets)

	if len(in.Volumes) > 0 && in.Volumes[0].Root {
		m.RootVolumeSize = types.Int64Value(in.Volumes[0].Size)
		m.Datastore = r.datastoreName(ctx, p, in.ProjectID, in.Volumes[0].DatastoreID, m.Datastore)
	}
	prior := volumesOf(ctx, m.DataVolumes, diags)
	var vols []dataVolumeModel
	for _, v := range in.Volumes {
		if v.Root {
			continue
		}
		var was types.String
		if n := len(vols); n < len(prior) {
			was = prior[n].Datastore
		}
		vols = append(vols, dataVolumeModel{Name: types.StringValue(v.Name), Size: types.Int64Value(v.Size),
			Datastore: r.datastoreName(ctx, p, in.ProjectID, v.DatastoreID, was)})
	}
	if len(vols) > 0 || !m.DataVolumes.IsNull() {
		m.DataVolumes, _ = types.ListValueFrom(ctx, dataVolumeType, vols)
	}
	if len(vols) == 0 && m.DataVolumes.IsUnknown() {
		m.DataVolumes = types.ListNull(dataVolumeType)
	}

	m.Environment = r.environmentName(ctx, in.Environment, m.Environment)
	m.Domain = r.domainName(ctx, in.NetworkDomainID, m.Domain)
	m.Labels = setOrNull(ctx, in.Labels, m.Labels)
	m.Tags = mapOrNull(ctx, in.Tags, m.Tags)
	user := map[string]string{}
	for k, v := range in.EnvVars {
		if !in.IsPlatformEnvVar(k) {
			user[k] = v
		}
	}
	m.EnvVars = mapOrNull(ctx, user, m.EnvVars)

	groups, err := r.client.InstanceSecurityGroups(ctx, in.ID)
	if err != nil {
		diags.AddError("Unable to read the instance's security groups", err.Error())
		return
	}
	m.SecurityGroups = securityGroupNames(ctx, groups, m.SecurityGroups)

	th, err := r.client.InstanceThreshold(ctx, in.ID)
	if err != nil {
		diags.AddError("Unable to read the instance's scaling", err.Error())
		return
	}
	m.Scale = scaleOf(ctx, th)
}

// refreshProject keeps the configured project when it is the same one (short or full name), and
// otherwise (drift, import) shows the short name.
func (r *instanceResource) refreshProject(ctx context.Context, m *instanceModel, p *client.Placement, projectID int64, diags *diag.Diagnostics) {
	if known(m.Project) {
		if cur, err := r.client.ProjectByNameAt(ctx, p, m.Project.ValueString()); err == nil && cur.ID == projectID {
			return
		}
	}
	projects, err := r.client.ProjectsAt(ctx, p)
	if err != nil {
		diags.AddError("Unable to read the instance's project", err.Error())
		return
	}
	who, err := r.client.WhoAmI(ctx)
	if err != nil {
		diags.AddError("Unable to read your tenant", err.Error())
		return
	}
	for _, pr := range projects {
		if pr.ID == projectID {
			m.Project = types.StringValue(normalize.ProjectShortName(pr.Name, who.AccountName))
			return
		}
	}
	m.Project = types.StringValue(strconv.FormatInt(projectID, 10))
}

// datastoreName maps a volume's datastore back to a name: "auto" is the default (null unless the
// configuration named the default datastore), and an ID keeps the configured spelling.
func (r *instanceResource) datastoreName(ctx context.Context, p *client.Placement, projectID int64, id string, was types.String) types.String {
	if id == "auto" || id == "" {
		if known(was) {
			return was
		}
		return types.StringNull()
	}
	stores, err := r.client.DatastoresAt(ctx, p, projectID)
	if err != nil {
		return was
	}
	for _, s := range stores {
		if strconv.FormatInt(s.ID, 10) == id {
			if known(was) && strings.EqualFold(was.ValueString(), s.Name) {
				return was
			}
			return types.StringValue(s.Name)
		}
	}
	return types.StringValue(id)
}

func (r *instanceResource) environmentName(ctx context.Context, code string, was types.String) types.String {
	if code == "" {
		return types.StringNull()
	}
	if known(was) {
		if env, err := r.client.EnvironmentByName(ctx, was.ValueString()); err == nil && env.Value == code {
			return was
		}
	}
	envs, err := r.client.Environments(ctx)
	if err == nil {
		for _, e := range envs {
			if e.Value == code {
				return types.StringValue(e.Name)
			}
		}
	}
	return types.StringValue(code)
}

func (r *instanceResource) domainName(ctx context.Context, id int64, was types.String) types.String {
	if id == 0 {
		return types.StringNull()
	}
	if known(was) {
		if d, err := r.client.NetworkDomainByName(ctx, was.ValueString()); err == nil && d.ID == id {
			return was
		}
	}
	domains, err := r.client.NetworkDomains(ctx)
	if err == nil {
		for _, d := range domains {
			if d.ID == id {
				return types.StringValue(d.Name)
			}
		}
	}
	return types.StringValue(strconv.FormatInt(id, 10))
}

// securityGroupNames is the applied groups as the configuration names them. The project's default
// group is applied by the provider, so it only shows when the configuration lists it too.
func securityGroupNames(ctx context.Context, groups []client.Option, was types.Set) types.Set {
	prior := map[string]string{}
	for _, v := range was.Elements() {
		if s, ok := v.(types.String); ok && !s.IsUnknown() {
			prior[strings.ToLower(s.ValueString())] = s.ValueString()
		}
	}
	var names []string
	for _, g := range groups {
		spelled, listed := prior[strings.ToLower(g.Name)]
		switch {
		case listed:
			names = append(names, spelled)
		case !client.IsDefaultSecurityGroup(g.Name):
			names = append(names, g.Name)
		}
	}
	if len(names) == 0 && was.IsNull() {
		return types.SetNull(types.StringType)
	}
	set, _ := types.SetValueFrom(ctx, types.StringType, names)
	return set
}

func setOrNull(ctx context.Context, vals []string, was types.Set) types.Set {
	if len(vals) == 0 && (was.IsNull() || was.IsUnknown()) {
		return types.SetNull(types.StringType)
	}
	set, _ := types.SetValueFrom(ctx, types.StringType, append([]string{}, vals...))
	return set
}

func mapOrNull(ctx context.Context, vals map[string]string, was types.Map) types.Map {
	if len(vals) == 0 && (was.IsNull() || was.IsUnknown()) {
		return types.MapNull(types.StringType)
	}
	m, _ := types.MapValueFrom(ctx, types.StringType, vals)
	return m
}

func optional(s string) types.String {
	if s == "" {
		return types.StringNull()
	}
	return types.StringValue(s)
}

func known(s types.String) bool { return !s.IsNull() && !s.IsUnknown() }

func allKnown(ss ...types.String) bool {
	for _, s := range ss {
		if !known(s) {
			return false
		}
	}
	return true
}

// sameName compares two optional names the way the platform does (case ignored); null equals null.
func sameName(a, b types.String) bool {
	if a.IsNull() || b.IsNull() {
		return a.IsNull() && b.IsNull()
	}
	return strings.EqualFold(a.ValueString(), b.ValueString())
}

func stringsOf(ctx context.Context, v interface {
	IsNull() bool
	IsUnknown() bool
	ElementsAs(context.Context, any, bool) diag.Diagnostics
}, diags *diag.Diagnostics) []string {
	if v.IsNull() || v.IsUnknown() {
		return nil
	}
	var out []string
	d := v.ElementsAs(ctx, &out, false)
	if diags != nil {
		diags.Append(d...)
	}
	return out
}

func mapOf(ctx context.Context, v types.Map, diags *diag.Diagnostics) map[string]string {
	if v.IsNull() || v.IsUnknown() {
		return nil
	}
	out := map[string]string{}
	diags.Append(v.ElementsAs(ctx, &out, false)...)
	return out
}

func volumesOf(ctx context.Context, v types.List, diags *diag.Diagnostics) []dataVolumeModel {
	if v.IsNull() || v.IsUnknown() {
		return nil
	}
	var out []dataVolumeModel
	d := v.ElementsAs(ctx, &out, false)
	if diags != nil {
		diags.Append(d...)
	}
	return out
}

// scaleAttribute is the scale block, Optional on both instance resources. It isn't Optional+Computed
// on the settings resource: removing an Optional+Computed nested attribute from the configuration
// plans an update every time (Terraform proposes null for it), so null means "not managed" there.
func scaleAttribute(description string) schema.SingleNestedAttribute {
	return schema.SingleNestedAttribute{
		Optional:    true,
		Description: description,
		Attributes: map[string]schema.Attribute{
			"min_count": schema.Int64Attribute{Required: true, Description: "Fewest VMs.",
				Validators: []validator.Int64{int64validator.AtLeast(1)}},
			"max_count": schema.Int64Attribute{Required: true, Description: "Most VMs.",
				Validators: []validator.Int64{int64validator.AtLeast(1)}},
			"increment": schema.Int64Attribute{Optional: true, Computed: true, Default: int64default.StaticInt64(1),
				Description: "VMs added or removed at a time. Defaults to 1.", Validators: []validator.Int64{int64validator.AtLeast(1)}},
			"auto_up": schema.BoolAttribute{Optional: true, Computed: true, Default: booldefault.StaticBool(true),
				Description: "Add VMs automatically. Defaults to `true`."},
			"auto_down": schema.BoolAttribute{Optional: true, Computed: true, Default: booldefault.StaticBool(true),
				Description: "Remove VMs automatically. Defaults to `true`."},
			"cpu":    usageRange("CPU"),
			"memory": usageRange("Memory"),
			"disk":   usageRange("Disk"),
		},
	}
}

// checkScale refuses a scale block that can't work: no metric to watch, or marks the wrong way round.
func checkScale(ctx context.Context, v types.Object, diags *diag.Diagnostics) {
	if v.IsNull() || v.IsUnknown() {
		return
	}
	var sc scaleModel
	diags.Append(v.As(ctx, &sc, basetypes.ObjectAsOptions{})...)
	at := path.Root("scale")
	if known64(sc.MinCount) && known64(sc.MaxCount) && sc.MaxCount.ValueInt64() < sc.MinCount.ValueInt64() {
		diags.AddAttributeError(at.AtName("max_count"), "Invalid scaling", "max_count must be at least min_count.")
	}
	if sc.CPU.IsNull() && sc.Memory.IsNull() && sc.Disk.IsNull() {
		diags.AddAttributeError(at, "Invalid scaling", "Watch at least one of cpu, memory or disk, or remove the scale block.")
	}
	for name, o := range map[string]types.Object{"cpu": sc.CPU, "memory": sc.Memory, "disk": sc.Disk} {
		if o.IsNull() || o.IsUnknown() {
			continue
		}
		var r rangeModel
		diags.Append(o.As(ctx, &r, basetypes.ObjectAsOptions{})...)
		if !r.Min.IsUnknown() && !r.Max.IsUnknown() && r.Min.ValueFloat64() >= r.Max.ValueFloat64() {
			diags.AddAttributeError(at.AtName(name), "Invalid scaling", fmt.Sprintf("%s: min must be below max.", name))
		}
	}
}

func known64(v types.Int64) bool { return !v.IsNull() && !v.IsUnknown() }

// thresholdOf is the planned scale block in API terms; null turns scaling off.
func thresholdOf(ctx context.Context, v types.Object, diags *diag.Diagnostics) client.Threshold {
	if v.IsNull() || v.IsUnknown() {
		return client.Threshold{}
	}
	var sc scaleModel
	diags.Append(v.As(ctx, &sc, basetypes.ObjectAsOptions{})...)
	rng := func(o types.Object) *client.Range {
		if o.IsNull() || o.IsUnknown() {
			return nil
		}
		var r rangeModel
		diags.Append(o.As(ctx, &r, basetypes.ObjectAsOptions{})...)
		return &client.Range{Min: r.Min.ValueFloat64(), Max: r.Max.ValueFloat64()}
	}
	return client.Threshold{
		AutoUp: sc.AutoUp.ValueBool(), AutoDown: sc.AutoDown.ValueBool(),
		MinCount: sc.MinCount.ValueInt64(), MaxCount: sc.MaxCount.ValueInt64(), Increment: sc.Increment.ValueInt64(),
		CPU: rng(sc.CPU), Memory: rng(sc.Memory), Disk: rng(sc.Disk),
	}
}

// scaleOf is the instance's scaling as the scale block; null when scaling is off.
func scaleOf(ctx context.Context, th *client.Threshold) types.Object {
	if !th.Set() {
		return types.ObjectNull(scaleType.AttrTypes)
	}
	rng := func(r *client.Range) types.Object {
		if r == nil {
			return types.ObjectNull(rangeType.AttrTypes)
		}
		o, _ := types.ObjectValueFrom(ctx, rangeType.AttrTypes, rangeModel{Min: types.Float64Value(r.Min), Max: types.Float64Value(r.Max)})
		return o
	}
	o, _ := types.ObjectValueFrom(ctx, scaleType.AttrTypes, scaleModel{
		MinCount: types.Int64Value(th.MinCount), MaxCount: types.Int64Value(th.MaxCount), Increment: types.Int64Value(th.Increment),
		AutoUp: types.BoolValue(th.AutoUp), AutoDown: types.BoolValue(th.AutoDown),
		CPU: rng(th.CPU), Memory: rng(th.Memory), Disk: rng(th.Disk),
	})
	return o
}

// fipChange is what a failed setFloatingIP is known to have done, for the caller to record.
type fipChange struct {
	released    bool // the old address is confirmed gone
	unconfirmed bool // an attach was sent but its address never showed: it may still arrive
}

// setFloatingIP moves the instance's VMs from one floating IP pool to another (either may be null):
// it detaches the old address and attaches one from the new pool, and waits for each change to show
// on the instance (owner, 2026-10-27: an attach that fails, fails the apply that ran it). An address
// the instance already has with no pool recorded (import, or attached in the console) is adopted.
func (r *instanceResource) setFloatingIP(ctx context.Context, inst *client.Instance, from, to types.String) (*client.Instance, fipChange, error) {
	var done fipChange
	if len(inst.PublicIPs) != len(inst.ContainerIDs) {
		if err := r.client.FillServerIPs(ctx, inst); err != nil {
			return inst, done, fmt.Errorf("reading the instance's servers: %w", err)
		}
	}
	if from.IsNull() && !inst.MissingPublicIP() {
		return inst, done, nil
	}
	if len(inst.ContainerIDs) == 0 {
		return inst, done, fmt.Errorf("instance %d has no VM to attach a floating IP to yet", inst.ID)
	}
	if !from.IsNull() {
		for _, c := range inst.ContainerIDs {
			// A 5xx or lost answer may still have released it: the wait below tells.
			if err := r.client.DetachFloatingIP(ctx, c); err != nil && !client.MayHaveSucceeded(err) {
				return inst, done, fmt.Errorf("detaching the floating IP: %w", err)
			}
		}
		got, err := r.client.WaitForPublicIP(ctx, inst.ID, false)
		if err != nil {
			return inst, done, fmt.Errorf("waiting for the floating IP to be released: %w%s", err, timeoutHint(err))
		}
		inst, done.released = got, true
	}
	if !known(to) {
		return inst, done, nil
	}
	for i, c := range inst.ContainerIDs {
		if !done.released && i < len(inst.PublicIPs) && inst.PublicIPs[i] != "" {
			continue // this server has one already (another was released in the console)
		}
		pool, err := r.client.FloatingIPPoolByName(ctx, c, to.ValueString())
		if err != nil {
			return inst, done, err
		}
		// A 5xx or lost answer may still have attached it: the wait below tells.
		err = r.client.AttachFloatingIP(ctx, c, pool.ID)
		if err != nil && !client.MayHaveSucceeded(err) {
			return inst, done, fmt.Errorf("attaching a floating IP from %s to VM %d of %d: %w. A floating IP needs "+
				"the instance's network to be on an MTN Router with an external network", pool.Name, i+1, len(inst.ContainerIDs), err)
		}
		done.unconfirmed = true
	}
	got, err := r.client.WaitForPublicIP(ctx, inst.ID, true)
	if err != nil {
		return inst, done, fmt.Errorf("waiting for the floating IP from %s: %w. Check that the instance's network is "+
			"on an MTN Router with an external network%s", to.ValueString(), err, timeoutHint(err))
	}
	done.unconfirmed = false
	return got, done, nil
}

// timeoutHint suggests a longer timeout when a wait ran out of time.
func timeoutHint(err error) string {
	if errors.Is(err, context.DeadlineExceeded) {
		return `. If MTN Cloud is slow, raise the timeout, for example timeouts { update = "60m" }`
	}
	return ""
}

// recordUnconfirmed keeps a floating IP whose attach was sent but never showed: Read then waits for
// it instead of attaching a second one.
func recordUnconfirmed(ctx context.Context, priv interface {
	SetKey(context.Context, string, []byte) diag.Diagnostics
}, diags *diag.Diagnostics) {
	diags.Append(priv.SetKey(ctx, fipUnconfirmedKey, []byte(`true`))...)
}

// fipUnconfirmedKey marks, in private state, an attach that was sent but whose address never showed.
const fipUnconfirmedKey = "floating_ip_unconfirmed"

// agentByRouter is install_agent's default, decided at create: on when the primary network is on a
// router with an external network (the agent can reach MTN Cloud), off otherwise. ok is false when
// the network can't be read from the plan. A router list that can't be read leaves the agent on, as before, with a warning.
func (r *instanceResource) agentByRouter(ctx context.Context, plan instanceModel, diags *diag.Diagnostics) (bool, bool) {
	if plan.Networks.IsUnknown() || plan.Networks.IsNull() || len(plan.Networks.Elements()) == 0 {
		return false, false
	}
	first, ok := plan.Networks.Elements()[0].(types.String)
	if !ok || !known(first) {
		return false, false
	}
	id, err := strconv.ParseInt(first.ValueString(), 10, 64)
	if err != nil {
		return false, false
	}
	on, err := r.client.HasExternalRouter(ctx, id)
	if err != nil {
		diags.AddAttributeWarning(path.Root("install_agent"), "Agent default not checked",
			"The routers couldn't be read to see whether the primary network reaches MTN Cloud, so the agent is installed: "+err.Error())
		return true, true
	}
	return on, true
}

const publicIPsDescription = "The floating IP of each server that has one, in server order (several with " +
	"`scale_factor` above 1). With one server, the same as `public_ip`. A server without one (released in the " +
	"console, or added by autoscaling) gets one on the next apply while `floating_ip_pool` is set."

// serverIPs is public_ips: each server's floating IP, read from its server when there are several.
func serverIPs(ctx context.Context, c *client.Client, in *client.Instance, diags *diag.Diagnostics) types.List {
	if in.PublicIPs == nil && len(in.ContainerIDs) > 1 {
		if err := c.FillServerIPs(ctx, in); err != nil {
			diags.AddWarning("Unable to read the instance's servers", "Their floating IPs aren't shown: "+err.Error())
		}
	}
	ips := []string{}
	for _, ip := range in.PublicIPs {
		if ip != "" {
			ips = append(ips, ip)
		}
	}
	if len(in.PublicIPs) == 0 && in.PublicIP != "" {
		ips = append(ips, in.PublicIP)
	}
	out, d := types.ListValueFrom(ctx, types.StringType, ips)
	diags.Append(d...)
	return out
}

// fipPartialKey keeps, in private state, the pool a Read set aside when only some servers lost
// their floating IP: the others still have addresses from it.
const fipPartialKey = "floating_ip_partial_pool"

type privateState interface {
	GetKey(context.Context, string) ([]byte, diag.Diagnostics)
}

type privateSetter interface {
	SetKey(context.Context, string, []byte) diag.Diagnostics
}

func rememberPartial(ctx context.Context, priv privateSetter, pool types.String, diags *diag.Diagnostics) {
	b, _ := json.Marshal(pool.ValueString())
	diags.Append(priv.SetKey(ctx, fipPartialKey, b)...)
}

// fromPool is the pool an attach moves away from: state's, or, when a Read set it aside because only
// some servers lost their address, that pool if the configuration now asks for another, so the
// servers that kept theirs move too. Null means only servers without an address get one.
func fromPool(ctx context.Context, priv privateState, state, to types.String, diags *diag.Diagnostics) types.String {
	if !state.IsNull() {
		return state
	}
	b, d := priv.GetKey(ctx, fipPartialKey)
	diags.Append(d...)
	var was string
	if len(b) == 0 || json.Unmarshal(b, &was) != nil || was == "" || sameName(types.StringValue(was), to) {
		return state
	}
	return types.StringValue(was)
}
