package loadbalancer

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/mtn/terraform-provider-mtncloud/internal/client"
)

// Resources lists every resource in this package, for the provider to register. Each piece of a
// load balancer (listener, pool, member, health monitor) is its own resource and changes only
// itself: the plugin's per-piece actions re-read the load balancer and save that one piece, so
// stale state can never delete a sibling (the plugin's TERRAFORM-AGENT-GUIDE.md, section 9.6).
func Resources() []func() resource.Resource {
	return []func() resource.Resource{
		newLoadBalancerResource, newListenerResource, newPoolResource, newMonitorResource, newMemberResource,
	}
}

// lbTimeout is the default for a write: the plugin waits up to 3 minutes for the cloud, and a load
// balancer itself took about 2 minutes to become ACTIVE live.
const lbTimeout = 20 * time.Minute

// rbase gives every resource its client.
type rbase struct{ client *client.Client }

func (b *rbase) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	c, ok := req.ProviderData.(*client.Client)
	if !ok {
		resp.Diagnostics.AddError("Unexpected provider data", fmt.Sprintf("expected *client.Client, got %T. This is a provider bug.", req.ProviderData))
		return
	}
	b.client = c
}

func (b *rbase) ready(diags *diag.Diagnostics) bool {
	if b.client == nil {
		diags.AddError("MTN Cloud provider is not configured yet",
			"The provider block uses values that are only known after apply. Use values known at plan time, or apply the resources they come from first.")
		return false
	}
	return true
}

var (
	replaceStr   = []planmodifier.String{stringplanmodifier.RequiresReplace()}
	replaceInt   = []planmodifier.Int64{int64planmodifier.RequiresReplace()}
	keepStr      = []planmodifier.String{stringplanmodifier.UseStateForUnknown()}
	keepInt      = []planmodifier.Int64{int64planmodifier.UseStateForUnknown()}
	portValid    = []validator.Int64{int64validator.Between(1, 65535)}
	listenerKind = "listener"
)

// removedReplaces is said of an Optional argument the cloud can't clear once set (a listener's
// certificate: guide 9.6), so taking it out of the configuration replaces the piece instead.
const removedReplaces = "Taking it out of the configuration replaces the resource: the cloud can't clear it."

func replaceIfRemovedStr() []planmodifier.String {
	return []planmodifier.String{stringplanmodifier.RequiresReplaceIf(func(_ context.Context, req planmodifier.StringRequest, resp *stringplanmodifier.RequiresReplaceIfFuncResponse) {
		resp.RequiresReplace = !req.StateValue.IsNull() && req.PlanValue.IsNull()
	}, removedReplaces, removedReplaces)}
}

// keptWhenRemoved ends the description of an Optional+Computed argument that keeps its state value
// when it is taken out of the configuration (UseStateForUnknown): the cloud has no "reset".
const keptWhenRemoved = " Taken out of the configuration, it keeps its last value."

func networkAttr() schema.Int64Attribute {
	return schema.Int64Attribute{
		Required: true, PlanModifiers: replaceInt,
		Description: "ID of the MTN Network the load balancer is on, for example `mtncloud_network.app.id`. Your tenant must own it: " +
			"a network shared with you is read-only. Changing it replaces the resource.",
	}
}

func lbIDAttr() schema.StringAttribute {
	return schema.StringAttribute{
		Required: true, PlanModifiers: replaceStr,
		Description: "ID of the load balancer, for example `mtncloud_load_balancer.web.id`. Changing it replaces the resource.",
	}
}

func idAttr(what string) schema.StringAttribute {
	return schema.StringAttribute{Computed: true, PlanModifiers: keepStr, Description: "The " + what + "'s ID."}
}

// nameAttrs are a piece's name and description: Optional+Computed with a default of "", because
// the cloud stores "" for none, so an unset value reads back the same.
func nameAttr(what string) schema.StringAttribute {
	return schema.StringAttribute{Optional: true, Computed: true, Default: stringdefault.StaticString(""), Description: "The " + what + "'s name."}
}

func descriptionAttr() schema.StringAttribute {
	return schema.StringAttribute{Optional: true, Computed: true, Default: stringdefault.StaticString(""), Description: "A description."}
}

// enabledAttr is Optional+Computed with a default: almost nobody sets it.
func enabledAttr(what string) schema.BoolAttribute {
	return schema.BoolAttribute{Optional: true, Computed: true, Default: booldefault.StaticBool(true),
		Description: "Whether the " + what + " takes traffic (administrative state). Defaults to `true`."}
}

// importIDs splits an import ID into its n parts: the network ID, then the cloud IDs.
func importIDs(req resource.ImportStateRequest, resp *resource.ImportStateResponse, form string, names ...string) {
	parts := strings.Split(req.ID, "/")
	bad := len(parts) != len(names)
	var net int64
	if !bad {
		var err error
		net, err = strconv.ParseInt(parts[0], 10, 64)
		bad = err != nil
	}
	for _, p := range parts {
		bad = bad || p == ""
	}
	if bad {
		resp.Diagnostics.AddError("Invalid import ID", fmt.Sprintf("Import it as %s. Got %q.", form, req.ID))
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(context.Background(), path.Root(names[0]), net)...)
	for i, n := range names[1:] {
		resp.Diagnostics.Append(resp.State.SetAttribute(context.Background(), path.Root(n), parts[i+1])...)
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(context.Background(), path.Root("id"), parts[len(parts)-1])...)
}

// readLB reads the load balancer once per run for every resource on it. gone reports one that no
// longer exists (or isn't on the network any more): its pieces are gone with it.
func (b *rbase) readLB(ctx context.Context, networkID int64, lbID string, diags *diag.Diagnostics) (lb *client.LoadBalancer, gone bool) {
	lb, _, err := b.client.LoadBalancerSnapshot(ctx, networkID, lbID, "")
	if client.IsLBMissing(err) {
		return nil, true
	}
	if err != nil {
		diags.AddError("Unable to read the load balancer", err.Error())
		return nil, false
	}
	return lb, false
}

// Conversions between plan values and the client's "nil means leave it" fields.

func strOf(v types.String) *string {
	if v.IsNull() || v.IsUnknown() {
		return nil
	}
	s := v.ValueString()
	return &s
}

// clearOf is strOf for a field the plugin clears with "": a value removed from the configuration
// removes it in the cloud.
func clearOf(v types.String) *string {
	if v.IsUnknown() {
		return nil
	}
	s := v.ValueString()
	return &s
}

func intOf(v types.Int64) *int64 {
	if v.IsNull() || v.IsUnknown() {
		return nil
	}
	i := v.ValueInt64()
	return &i
}

func boolOf(v types.Bool) *bool {
	if v.IsNull() || v.IsUnknown() {
		return nil
	}
	b := v.ValueBool()
	return &b
}

func strsOf(ctx context.Context, v types.Set) []string {
	if v.IsNull() || v.IsUnknown() {
		return nil
	}
	var out []string
	_ = v.ElementsAs(ctx, &out, false)
	return out
}

// optStr is "" read as null, for an Optional field the cloud stores as "" when unset.
func optStr(s string) types.String {
	if s == "" {
		return types.StringNull()
	}
	return types.StringValue(s)
}

// setOrNull reads a list as a set, keeping null when the configuration had none and the cloud has
// none, so an unset attribute reads back the same.
func setOrNull(was types.Set, vals []string) types.Set {
	if len(vals) == 0 && was.IsNull() {
		return types.SetNull(types.StringType)
	}
	elems := make([]attr.Value, 0, len(vals))
	for _, v := range vals {
		elems = append(elems, types.StringValue(v))
	}
	return types.SetValueMust(types.StringType, elems)
}

// missingAfterWrite reports a piece the load balancer doesn't show right after it was written,
// rather than saving the planned values as if it were there.
func missingAfterWrite(diags *diag.Diagnostics, kind string) {
	diags.AddError("The "+kind+" is missing after the change",
		"The change was accepted, but the load balancer doesn't show the "+kind+" any more. It may have been deleted at the "+
			"same time in the console. Run terraform plan to see what is there now.")
}
