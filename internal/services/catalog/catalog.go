// Package catalog holds the resources made by ordering a catalog item: projects first, then MOS and
// the other workflow items. Nothing here waits for what an order makes to sync into MTN Cloud (D-020).
package catalog

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/resource"

	"github.com/mtn/terraform-provider-mtncloud/internal/client"
)

// Resources lists every resource in this package, for the provider to register.
func Resources() []func() resource.Resource {
	return append([]func() resource.Resource{newProject, newNFSShare, newSMBShare}, orderResources()...)
}

// base gives every resource its client.
type base struct {
	client *client.Client
}

func (b *base) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return // validate or an unconfigured provider; CRUD checks ready()
	}
	c, ok := req.ProviderData.(*client.Client)
	if !ok {
		resp.Diagnostics.AddError("Unexpected provider data", fmt.Sprintf("expected *client.Client, got %T. This is a provider bug.", req.ProviderData))
		return
	}
	b.client = c
}

// ready reports whether the provider is configured; see lookup.base.ready for when it isn't.
func (b *base) ready(diags *diag.Diagnostics) bool {
	if b.client == nil {
		diags.AddError("MTN Cloud provider is not configured yet",
			"The provider block uses values that are only known after apply. Use values known at plan time, or apply the resources they come from first.")
		return false
	}
	return true
}

// Catalog items are found by code (MTN renames items: "MTN VPN Service" became "MTN PfSense VPN",
// 2026-10-30, code MVPNSV02 kept), and ordered by the name they have now.
var (
	projectItem = client.CatalogItemRef{Code: "001034sd", Names: []string{"Create Project"}}
	nfsItem     = client.CatalogItemRef{Code: "nfs12", Names: []string{"MTN NFS"}}
	smbItem     = client.CatalogItemRef{Code: "smb12", Names: []string{"MTN SMB"}}
)

// itemName is the item's name now, which an order is placed by.
func (b *base) itemName(ctx context.Context, ref client.CatalogItemRef) (string, error) {
	it, err := b.client.FindCatalogItem(ctx, ref)
	if err != nil {
		return "", err
	}
	return it.Name, nil
}

// isItem reports whether an inventory item was ordered from this catalog item: the same item ID, a
// name it has had, or its name now.
func (b *base) isItem(ctx context.Context, ref client.CatalogItemRef, item *client.OrderedItem) bool {
	if ref.Is(item.TypeName) {
		return true
	}
	it, err := b.client.FindCatalogItem(ctx, ref)
	return err == nil && (it.ID == item.TypeID || it.Name == item.TypeName)
}
