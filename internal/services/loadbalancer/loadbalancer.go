// Package loadbalancer holds the load balancer data sources. MTN Cloud reaches load balancers
// through one console task, not a REST endpoint (D-031); internal/client hides that. Every data
// source about one load balancer reads it once per run (LoadBalancerSnapshot), so listing its
// listeners, pools and monitors in one configuration costs one console job, not one each.
package loadbalancer

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/datasourcevalidator"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/mtn/terraform-provider-mtncloud/internal/client"
)

// DataSources lists every data source in this package, for the provider to register.
func DataSources() []func() datasource.DataSource {
	return []func() datasource.DataSource{
		newLoadBalancers, newLoadBalancer,
		newListeners, newListener,
		newPools, newPool,
		newMembers,
		newHealthMonitors, newHealthMonitor,
		newFloatingIPPools,
	}
}

// base gives every data source its client.
type base struct{ client *client.Client }

func (b *base) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
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

func (b *base) ready(diags *diag.Diagnostics) bool {
	if b.client == nil {
		diags.AddError("MTN Cloud provider is not configured yet",
			"The provider block uses values that are only known after apply, so this data source can't be read during plan. Use "+
				"values known at plan time (variables, environment variables), or apply the resources they come from first.")
		return false
	}
	return true
}

func networkIDAttribute() schema.Int64Attribute {
	return schema.Int64Attribute{
		Required: true,
		Description: "ID of the MTN Network the load balancer is on, for example `mtncloud_network.app.id`. Your tenant must own " +
			"the network or have it shared with it.",
	}
}

// lbSelectorAttributes picks one load balancer on the network, for the data sources about its parts.
func lbSelectorAttributes() map[string]schema.Attribute {
	return map[string]schema.Attribute{
		"network_id": networkIDAttribute(),
		// Optional+Computed: give one, and the other is filled in.
		"load_balancer_id": schema.StringAttribute{
			Optional: true, Computed: true, Description: "Load balancer ID. Give this or `load_balancer_name`.",
		},
		"load_balancer_name": schema.StringAttribute{
			Optional: true, Computed: true,
			Description: "Load balancer name, matched exactly; it must match exactly one on the network. Give this or `load_balancer_id`.",
		},
	}
}

func lbSelectorValidators() []datasource.ConfigValidator {
	return []datasource.ConfigValidator{
		datasourcevalidator.ExactlyOneOf(path.MatchRoot("load_balancer_id"), path.MatchRoot("load_balancer_name")),
	}
}

// readLB reads one load balancer and turns the plugin's refusals into errors on the attribute that
// caused them. idAttr and nameAttr name the selector attributes of the calling data source.
func (b *base) readLB(ctx context.Context, networkID int64, id, name types.String, idAttr, nameAttr string, diags *diag.Diagnostics) (*client.LoadBalancer, bool) {
	lb, canManage, err := b.client.LoadBalancerSnapshot(ctx, networkID, id.ValueString(), name.ValueString())
	if err == nil {
		return lb, canManage
	}
	switch {
	case errors.Is(err, client.ErrLBAccess):
		diags.AddError("Load balancers are not available to this login", err.Error())
	case client.IsLBNotAvailable(err):
		diags.AddAttributeError(path.Root("network_id"), "Network not available", err.Error())
	case client.IsLBMissing(err) && !id.IsNull():
		diags.AddAttributeError(path.Root(idAttr), "No such load balancer", err.Error())
	case client.IsLBMissing(err):
		diags.AddAttributeError(path.Root(nameAttr), "Load balancer not found", err.Error()+" mtncloud_load_balancers lists the network's load balancers.")
	default:
		diags.AddError("Unable to read the load balancer", err.Error())
	}
	return nil, false
}

// pick returns the one item whose ID or name matches. what names the item for errors ("listener"),
// attr the attribute prefix ("listener" for listener_id / listener_name, "" for id / name).
func pick[T any](items []T, idOf, nameOf func(T) string, id, name types.String, what, attr string, diags *diag.Diagnostics) (T, bool) {
	var zero T
	idAttr, nameAttr := "id", "name"
	if attr != "" {
		idAttr, nameAttr = attr+"_id", attr+"_name"
	}
	var found []T
	for _, it := range items {
		if (!id.IsNull() && idOf(it) == id.ValueString()) || (id.IsNull() && nameOf(it) == name.ValueString()) {
			found = append(found, it)
		}
	}
	switch {
	case len(found) == 1:
		return found[0], true
	case len(found) == 0 && !id.IsNull():
		diags.AddAttributeError(path.Root(idAttr), "No such "+what, fmt.Sprintf("The load balancer has no %s with ID %s.", what, id.ValueString()))
	case len(found) == 0:
		diags.AddAttributeError(path.Root(nameAttr), "No such "+what,
			fmt.Sprintf("The load balancer has no %s named %q. Names are matched exactly.", what, name.ValueString()))
	default:
		ids := make([]string, len(found))
		for i, f := range found {
			ids[i] = idOf(f)
		}
		diags.AddAttributeError(path.Root(nameAttr), "Several "+what+"s match",
			fmt.Sprintf("%d %ss are named %q (IDs %s). Look one up by %s.", len(found), what, name.ValueString(), strings.Join(ids, ", "), idAttr))
	}
	return zero, false
}

// merge returns one attribute map from several; later maps win, and a nil value removes the key.
func merge(maps ...map[string]schema.Attribute) map[string]schema.Attribute {
	out := map[string]schema.Attribute{}
	for _, m := range maps {
		for k, v := range m {
			if v == nil {
				delete(out, k) // a nil attribute removes one a shared map added
				continue
			}
			out[k] = v
		}
	}
	return out
}
