package compute

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
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/mapplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

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
	Status         types.String   `tfsdk:"status"`
	PrivateIP      types.String   `tfsdk:"private_ip"`
	PublicIP       types.String   `tfsdk:"public_ip"`
	Timeouts       timeouts.Value `tfsdk:"timeouts"`
}

type dataVolumeModel struct {
	Name      types.String `tfsdk:"name"`
	Size      types.Int64  `tfsdk:"size"`
	Datastore types.String `tfsdk:"datastore"`
}

var dataVolumeType = types.ObjectType{AttrTypes: map[string]attr.Type{
	"name": types.StringType, "size": types.Int64Type, "datastore": types.StringType,
}}

func (r *instanceResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_instance"
}

func (r *instanceResource) Schema(ctx context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	keep := []planmodifier.String{stringplanmodifier.UseStateForUnknown()}
	resp.Schema = schema.Schema{
		Description: "An MTN Cloud instance: an OpenStack virtual machine in one of your projects. Creating it waits until " +
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
			"group": schema.StringAttribute{
				Required: true,
				Description: "Group the instance is provisioned into, by name, for example `MTNNG_CLOUD_AZ_1`. Changing it " +
					"replaces the instance.",
			},
			"project": schema.StringAttribute{
				Required: true,
				Description: "Project the instance runs in, by name (for example `web-prod`; the full name with the tenant " +
					"suffix also works). Changing it to another project replaces the instance.",
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
					"primary network: changing it replaces the instance. Networks added at the end are attached in place (the " +
					"guest OS must configure the new card); removing or reordering networks replaces the instance.",
				Validators: []validator.List{
					listvalidator.SizeAtLeast(1),
					listvalidator.UniqueValues(),
					listvalidator.ValueStringsAre(stringvalidator.RegexMatches(digits, "must be a network ID")),
				},
			},
			"security_groups": schema.SetAttribute{
				Optional: true, ElementType: types.StringType,
				Description: "Security groups to apply, by name, in addition to the project's default group, which is " +
					"always applied. Changed in place. Each group needs the project as a location.",
			},
			// Optional+Computed: the plan's root size (10 GB) applies when left out.
			"root_volume_size": schema.Int64Attribute{
				Optional: true, Computed: true,
				Description: "Root volume size in GB. Left out, the plan's size applies (10 GB). MTN Cloud can't resize a " +
					"root volume: add a `data_volume` instead. Windows images need at least 40 GB.",
				Validators:    []validator.Int64{int64validator.AtLeast(1)},
				PlanModifiers: []planmodifier.Int64{int64planmodifier.UseStateForUnknown()},
			},
			"datastore": schema.StringAttribute{
				Optional: true,
				Description: "Datastore of the root volume, by name, for example `ENCR_AT_REST` (the `mtncloud_datastores` " +
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
			"install_agent": schema.BoolAttribute{
				Optional: true, Computed: true, Default: booldefault.StaticBool(true),
				Description: "Install the MTN Cloud agent, for monitoring, logs and automation. Defaults to `true`. The agent " +
					"needs the network to be on an MTN Router. Changing it replaces the instance.",
				PlanModifiers: []planmodifier.Bool{boolplanmodifier.RequiresReplace()},
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
			"status":     schema.StringAttribute{Computed: true, Description: "Status, for example `running`.", PlanModifiers: keep},
			"private_ip": schema.StringAttribute{Computed: true, Description: "Private IP address on the primary network.", PlanModifiers: keep},
			"public_ip":  schema.StringAttribute{Computed: true, Description: "Floating (public) IP address, when the instance has one.", PlanModifiers: keep},
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

// ModifyPlan checks names at plan time (rule 17) and decides what a change replaces. The API can't
// change the primary network, an existing disk or the root size; changing a reference that names
// the same thing ("web-prod" vs "web-prod-Acme", an image's name vs its code) is no change at all.
func (r *instanceResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.Plan.Raw.IsNull() || r.client == nil {
		return // destroy, or validation before the provider is configured
	}
	var plan instanceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
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
		resp.Diagnostics.AddAttributeError(path.Root("project"), "Unknown project", err.Error())
		return
	}
	image, err := r.client.ImageByName(ctx, plan.Image.ValueString())
	if err != nil {
		resp.Diagnostics.AddAttributeError(path.Root("image"), "Unknown image", err.Error())
		return
	}
	r.checkNames(ctx, plan, p, project.ID, image, &resp.Diagnostics)
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
	if !appendOnly(plan.Networks, state.Networks) {
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
	if resp.Diagnostics.HasError() || resp.Plan.Raw.Equal(req.State.Raw) {
		return
	}
	resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("status"), types.StringUnknown())...)
	resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("public_ip"), types.StringUnknown())...)
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
}

// appendOnly reports whether the planned list keeps every element of the state list in place,
// only adding to the end. Unknown values are treated as a change.
func appendOnly(plan, state types.List) bool {
	if plan.IsUnknown() {
		return false
	}
	p, s := plan.Elements(), state.Elements()
	if len(p) < len(s) {
		return false
	}
	for i := range s {
		if !p[i].Equal(s[i]) {
			return false
		}
	}
	return true
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
		diags.AddAttributeError(path.Root("project"), "Unknown project", err.Error())
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
	def, ok, err := r.client.DefaultSecurityGroup(ctx, p, project.ID)
	if err != nil {
		diags.AddError("Unable to read the project's security groups", err.Error())
		return out, false
	}
	if ok {
		in.SecurityGroupIDs = append(in.SecurityGroupIDs, def.ID)
	}
	for _, name := range stringsOf(ctx, m.SecurityGroups, diags) {
		g, err := r.client.SecurityGroupByName(ctx, p, project.ID, name)
		if err != nil {
			diags.AddAttributeError(path.Root("security_groups"), "Unknown security group", err.Error())
			return out, false
		}
		if !slices.Contains(in.SecurityGroupIDs, g.ID) {
			in.SecurityGroupIDs = append(in.SecurityGroupIDs, g.ID)
		}
	}
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
	r.settle(ctx, &plan, inst, err, "create", &resp.Diagnostics)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
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
	for _, s := range []*types.String{&m.Name, &m.Hostname, &m.Domain, &m.Status, &m.PrivateIP, &m.PublicIP} {
		if s.IsUnknown() {
			*s = types.StringNull()
		}
	}
	if m.RootVolumeSize.IsUnknown() {
		m.RootVolumeSize = types.Int64Null()
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
	if !plan.SecurityGroups.Equal(state.SecurityGroups) {
		if err := r.client.SetInstanceSecurityGroups(ctx, id, in.SecurityGroupIDs); err != nil {
			resp.Diagnostics.AddError("Unable to set the instance's security groups", err.Error())
			return
		}
	}
	if current.PlanID != in.PlanID || len(in.NetworkIDs) > len(current.Interfaces) || len(in.Volumes) > len(current.Volumes) {
		resize := client.ResizeInput{PlanID: in.PlanID}
		// Existing volumes and cards go with their IDs, new ones (at the end) without.
		for i, v := range in.Volumes {
			if i < len(current.Volumes) {
				cur := current.Volumes[i]
				v = client.VolumeSpec{ID: cur.ID, Root: cur.Root, Name: cur.Name, Size: cur.Size, DatastoreID: cur.DatastoreID}
			}
			resize.Volumes = append(resize.Volumes, v)
		}
		for i, n := range in.NetworkIDs {
			nic := client.Interface{NetworkID: n}
			if i < len(current.Interfaces) {
				nic = current.Interfaces[i]
			}
			resize.Interfaces = append(resize.Interfaces, nic)
		}
		if err := r.client.ResizeInstance(ctx, id, resize); err != nil {
			resp.Diagnostics.AddError("Unable to reconfigure instance", err.Error())
			return
		}
	}
	inst, err := r.client.WaitForInstance(ctx, id)
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
	m.PrivateIP = optional(in.PrivateIP)
	m.PublicIP = optional(in.PublicIP)
	m.InstallAgent = types.BoolValue(!in.NoAgent)
	m.ScaleFactor = types.Int64Value(in.LayoutSize)
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
