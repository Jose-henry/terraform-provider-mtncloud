package legacy

import (
	"context"
	"log"
	"net/http"

	sdk "github.com/gomorpheus/morpheus-go-sdk"
	"github.com/hashicorp/terraform-plugin-sdk/v2/diag"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
)

func dataSourceNetworkGroup() *schema.Resource {
	return &schema.Resource{
		Description: "Provides an MTN Cloud network group data source: looks up a group of networks by name or ID.",
		ReadContext: dataSourceNetworkGroupRead,
		Schema: map[string]*schema.Schema{
			"id": {
				Type:          schema.TypeInt,
				Optional:      true,
				ConflictsWith: []string{"name"},
				Computed:      true,
			},
			"name": {
				Type:          schema.TypeString,
				Description:   "The name of the MTN Cloud network group",
				Optional:      true,
				ConflictsWith: []string{"id"},
			},
			"active": {
				Type:        schema.TypeBool,
				Description: "Whether the network group is active or not",
				Computed:    true,
			},
			"description": {
				Type:        schema.TypeString,
				Description: "The description of the network group",
				Computed:    true,
			},
			"visibility": {
				Type:        schema.TypeString,
				Description: "Whether the network group is visible in sub-tenants or not",
				Computed:    true,
			},
		},
	}
}

func dataSourceNetworkGroupRead(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	client := meta.(*sdk.Client)

	// Warning or errors can be collected in a slice type
	var diags diag.Diagnostics

	name := d.Get("name").(string)
	id := d.Get("id").(int)

	// lookup by name if we do not have an id yet
	var resp *sdk.Response
	var err error
	if id == 0 && name != "" {
		resp, err = client.FindNetworkGroupByName(name)
	} else if id != 0 {
		resp, err = client.GetNetworkGroup(int64(id), &sdk.Request{})
	} else {
		return diag.Errorf("Network group cannot be read without name or id")
	}
	if err != nil {
		if id == 0 {
			if nd := nameLookupError(client, err, "network group", name, sdk.NetworkGroupsPath); nd != nil {
				return nd
			}
		}
		if resp != nil && resp.StatusCode == http.StatusNotFound && id != 0 {
			return notFoundByID("network group", int64(id))
		} else {
			log.Printf("API FAILURE: %s - %v", resp, err)
			return diag.FromErr(err)
		}
	}
	log.Printf("API RESPONSE: %s", resp)

	// store resource data
	result := resp.Result.(*sdk.GetNetworkGroupResult)
	networkGroup := result.NetworkGroup
	if networkGroup != nil {
		d.SetId(int64ToString(networkGroup.ID))
		setAttr(ctx, d, "name", networkGroup.Name)
		setAttr(ctx, d, "active", networkGroup.Active)
		setAttr(ctx, d, "description", networkGroup.Description)
		setAttr(ctx, d, "visibility", networkGroup.Visibility)
	} else {
		return diag.Errorf("Network group not found in response data.") // should not happen
	}
	return diags
}
