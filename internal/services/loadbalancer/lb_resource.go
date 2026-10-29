package loadbalancer

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-timeouts/resource/timeouts"
	"github.com/hashicorp/terraform-plugin-framework-validators/setvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/mtn/terraform-provider-mtncloud/internal/client"
	"github.com/mtn/terraform-provider-mtncloud/internal/validators"
)

var _ resource.ResourceWithImportState = (*lbResource)(nil)

func newLoadBalancerResource() resource.Resource { return &lbResource{} }

type lbResource struct{ rbase }

type lbModel struct {
	ID                 types.String   `tfsdk:"id"`
	NetworkID          types.Int64    `tfsdk:"network_id"`
	Name               types.String   `tfsdk:"name"`
	Description        types.String   `tfsdk:"description"`
	VIPSubnetID        types.String   `tfsdk:"vip_subnet_id"`
	VIPAddress         types.String   `tfsdk:"vip_address"`
	Tags               types.Set      `tfsdk:"tags"`
	Enabled            types.Bool     `tfsdk:"enabled"`
	ProvisioningStatus types.String   `tfsdk:"provisioning_status"`
	OperatingStatus    types.String   `tfsdk:"operating_status"`
	Timeouts           timeouts.Value `tfsdk:"timeouts"`
}

// requestTagPrefix marks the tag the plugin adds itself (a create's requestId). It isn't the
// user's, so it is never read into tags.
const requestTagPrefix = "mtncloud-req-"

func (r *lbResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_load_balancer"
}

func (r *lbResource) Schema(ctx context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "A load balancer on an MTN Network: a virtual IP address (VIP) that shares traffic across servers. Its listeners, " +
			"pools, members and health monitors are their own resources. Creating one takes about 2 minutes. The name, description, " +
			"tags and `enabled` change in place; the network, VIP subnet and VIP address replace it, with everything on it.",
		Attributes: map[string]schema.Attribute{
			"id":         idAttr("load balancer"),
			"network_id": networkAttr(),
			"name": schema.StringAttribute{Required: true, Validators: []validator.String{stringvalidator.LengthBetween(1, 255)},
				Description: "The load balancer's name."},
			// Optional+Computed with a default, so removing it from the configuration clears it.
			"description": schema.StringAttribute{Optional: true, Computed: true,
				Default: stringdefault.StaticString(""), Description: "A description."},
			// Optional+Computed: defaults to the network's own subnet.
			"vip_subnet_id": schema.StringAttribute{Optional: true, Computed: true, PlanModifiers: append(keepStr, replaceStr...),
				Description: "The cloud ID of the subnet the VIP is on. Defaults to the network's subnet. Changing it replaces the load balancer."},
			// Optional+Computed: the cloud picks a free address when it isn't set.
			"vip_address": schema.StringAttribute{Optional: true, Computed: true, PlanModifiers: append(keepStr, replaceStr...),
				Validators:  []validator.String{validators.IPv4Address()},
				Description: "The VIP's address on the subnet. Left unset, the cloud picks a free one. Changing it replaces the load balancer."},
			"tags": schema.SetAttribute{Optional: true, ElementType: types.StringType,
				Validators:  []validator.Set{setvalidator.SizeBetween(1, 50), setvalidator.ValueStringsAre(stringvalidator.LengthBetween(1, 255))},
				Description: "Tags on the load balancer, at most 50."},
			// Optional+Computed with a default: a load balancer serves traffic unless told not to.
			"enabled": schema.BoolAttribute{Optional: true, Computed: true, Default: booldefault.StaticBool(true),
				Description: "Whether the load balancer passes traffic. Defaults to `true`. **`false` takes the whole load balancer out " +
					"of service**: every listener stops passing traffic until it is set back to `true`."},
			"provisioning_status": schema.StringAttribute{Computed: true, Description: "`ACTIVE` once it is ready; `ERROR` if the cloud failed it."},
			"operating_status":    schema.StringAttribute{Computed: true, Description: "`ONLINE` when its members answer their health checks; `OFFLINE` with no members."},
		},
		Blocks: map[string]schema.Block{
			"timeouts": timeouts.Block(ctx, timeouts.Opts{Create: true, Update: true, Delete: true}),
		},
	}
}

func (r *lbResource) fill(m *lbModel, lb *client.LoadBalancer) {
	m.ID = types.StringValue(lb.ID)
	m.Name = types.StringValue(lb.Name)
	m.Description = types.StringValue(lb.Description)
	m.VIPSubnetID = types.StringValue(lb.VIPSubnetID)
	m.VIPAddress = types.StringValue(lb.VIPAddress)
	var tags []string
	for _, t := range lb.Tags {
		if !strings.HasPrefix(t, requestTagPrefix) {
			tags = append(tags, t)
		}
	}
	m.Tags = setOrNull(m.Tags, tags)
	m.Enabled = types.BoolValue(lb.AdminStateUp)
	m.ProvisioningStatus = types.StringValue(lb.ProvisioningStatus)
	m.OperatingStatus = types.StringValue(lb.OperatingStatus)
}

func (r *lbResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	if !r.ready(&resp.Diagnostics) {
		return
	}
	var plan lbModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	timeout, d := plan.Timeouts.Create(ctx, lbTimeout)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	net := plan.NetworkID.ValueInt64()
	subnet := plan.VIPSubnetID.ValueString()
	if plan.VIPSubnetID.IsUnknown() || plan.VIPSubnetID.IsNull() {
		n, err := r.client.GetNetwork(ctx, net)
		if err != nil {
			resp.Diagnostics.AddAttributeError(path.Root("network_id"), "Unable to read the network", err.Error())
			return
		}
		if n.SubnetID == 0 {
			resp.Diagnostics.AddAttributeError(path.Root("vip_subnet_id"), "The network has no subnet",
				fmt.Sprintf("MTN Network %d has no subnet yet, so the VIP has nowhere to go. Set vip_subnet_id, or wait until the network is ready.", net))
			return
		}
		if subnet, err = r.client.SubnetExternalID(ctx, n.SubnetID); err != nil {
			resp.Diagnostics.AddAttributeError(path.Root("vip_subnet_id"), "Unable to read the network's subnet", err.Error())
			return
		}
	}
	// One request ID per create: the plugin tags the new load balancer with it, so a retried create
	// finds that one instead of making a second (guide 9.2).
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	lb, err := r.client.CreateLoadBalancer(ctx, net, "tf"+hex.EncodeToString(b), client.LoadBalancerSpec{
		Name: plan.Name.ValueString(), Description: plan.Description.ValueString(), VIPSubnetID: subnet,
		VIPAddress: plan.VIPAddress.ValueString(), Tags: strsOf(ctx, plan.Tags),
	})
	if lb == nil || lb.ID == "" {
		resp.Diagnostics.AddError("Unable to create the load balancer", err.Error())
		return
	}
	// Kept even when it failed, so Terraform replaces it rather than leaving it behind.
	enabled := plan.Enabled
	r.fill(&plan, lb)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
	if err != nil {
		resp.Diagnostics.AddError("Unable to create the load balancer", err.Error())
		return
	}
	// A create can't start disabled, so a disabled one is switched off straight after.
	if !enabled.ValueBool() && lb.AdminStateUp {
		off := false
		if err := r.client.UpdateLoadBalancer(ctx, net, lb.ID, client.LoadBalancerUpdate{Enabled: &off}); err != nil {
			resp.Diagnostics.AddAttributeError(path.Root("enabled"), "Unable to disable the new load balancer", err.Error())
		}
		r.refresh(ctx, &plan, &resp.State, &resp.Diagnostics)
	}
}

// refresh reads the load balancer again after a write and saves it into state.
func (r *lbResource) refresh(ctx context.Context, m *lbModel, st *tfsdk.State, diags *diag.Diagnostics) {
	lb, gone := r.readLB(ctx, m.NetworkID.ValueInt64(), m.ID.ValueString(), diags)
	if gone {
		missingAfterWrite(diags, "load balancer")
	}
	if lb != nil {
		r.fill(m, lb)
	}
	diags.Append(st.Set(ctx, m)...)
}

func (r *lbResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	if !r.ready(&resp.Diagnostics) {
		return
	}
	var state lbModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	lb, gone := r.readLB(ctx, state.NetworkID.ValueInt64(), state.ID.ValueString(), &resp.Diagnostics)
	if gone {
		resp.State.RemoveResource(ctx)
		return
	}
	if lb == nil {
		return
	}
	r.fill(&state, lb)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Update changes the load balancer's own fields with one loadbalancer-update, sending only what
// changed. Its listeners and pools are left alone.
func (r *lbResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	if !r.ready(&resp.Diagnostics) {
		return
	}
	var plan, state lbModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	timeout, d := plan.Timeouts.Update(ctx, lbTimeout)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var u client.LoadBalancerUpdate
	changed := false
	if !plan.Name.Equal(state.Name) {
		u.Name, changed = strOf(plan.Name), true
	}
	if !plan.Description.Equal(state.Description) {
		u.Description, changed = strOf(plan.Description), true
	}
	if !plan.Tags.Equal(state.Tags) {
		tags := nonNilStrs(strsOf(ctx, plan.Tags)) // none left: [] removes them
		u.Tags, changed = &tags, true
	}
	if !plan.Enabled.Equal(state.Enabled) {
		u.Enabled, changed = boolOf(plan.Enabled), true
	}
	plan.ID = state.ID
	if changed {
		if err := r.client.UpdateLoadBalancer(ctx, plan.NetworkID.ValueInt64(), plan.ID.ValueString(), u); err != nil {
			resp.Diagnostics.AddError("Unable to change the load balancer", err.Error())
			return
		}
	}
	r.refresh(ctx, &plan, &resp.State, &resp.Diagnostics)
}

func nonNilStrs(v []string) []string {
	if v == nil {
		return []string{}
	}
	return v
}

func (r *lbResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	if !r.ready(&resp.Diagnostics) {
		return
	}
	var state lbModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	timeout, d := state.Timeouts.Delete(ctx, lbTimeout)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if err := r.client.DeleteLoadBalancer(ctx, state.NetworkID.ValueInt64(), state.ID.ValueString()); err != nil {
		resp.Diagnostics.AddError("Unable to delete the load balancer", err.Error())
	}
}

func (r *lbResource) ImportState(_ context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	importIDs(req, resp, "<network_id>/<load_balancer_id>", "network_id", "id")
}
