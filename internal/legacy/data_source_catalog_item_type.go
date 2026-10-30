package legacy

import (
	"context"
	"fmt"
	"log"

	sdk "github.com/gomorpheus/morpheus-go-sdk"
	"github.com/hashicorp/terraform-plugin-sdk/v2/diag"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
)

func dataSourceCatalogItemType() *schema.Resource {
	return &schema.Resource{
		Description: "Provides an MTN Cloud catalog item type data source: looks up a self-service catalog item type by name or ID.",
		ReadContext: dataSourceCatalogItemTypeRead,
		Schema: map[string]*schema.Schema{
			"id": {
				Type:          schema.TypeInt,
				Description:   "The ID of the catalog item type",
				Optional:      true,
				ConflictsWith: []string{"name"},
				Computed:      true,
			},
			"name": {
				Type:          schema.TypeString,
				Description:   "The name of the catalog item type",
				Optional:      true,
				ConflictsWith: []string{"id"},
			},
		},
	}
}

func dataSourceCatalogItemTypeRead(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	client := meta.(*sdk.Client)

	// Warning or errors can be collected in a slice type
	var diags diag.Diagnostics

	name := d.Get("name").(string)
	id := d.Get("id").(int)

	// lookup by name if we do not have an id yet
	var resp *sdk.Response
	var err error
	if id == 0 && name != "" {
		// Find by name, then get by ID
		listResp, listErr := client.ListCatalogItemTypes(&sdk.Request{
			QueryParams: map[string]string{
				"name": name,
			},
		})
		if listErr != nil {
			return diag.FromErr(listErr)
		}
		listResult := listResp.Result.(*sdk.ListCatalogItemTypesResult)
		catalogItemTypeCount := len(*listResult.CatalogItemTypes)
		if catalogItemTypeCount != 1 {
			return diag.FromErr(fmt.Errorf("found %d catalog item types for %v", catalogItemTypeCount, name))
		}
		firstRecord := (*listResult.CatalogItemTypes)[0]
		catalogItemTypeID := firstRecord.Id
		resp, err = client.GetCatalogItemType(catalogItemTypeID, &sdk.Request{})
	} else if id != 0 {
		resp, err = client.GetCatalogItemType(int64(id), &sdk.Request{})
	} else {
		return diag.Errorf("Catalog Item Type cannot be read without name or id")
	}
	if err != nil {
		if resp != nil && resp.StatusCode == 404 {
			log.Printf("API 404: %s - %v", resp, err)
			return nil
		} else {
			log.Printf("API FAILURE: %s - %v", resp, err)
			return diag.FromErr(err)
		}
	}
	log.Printf("API RESPONSE: %s", resp)

	// store resource data
	result := resp.Result.(*sdk.GetCatalogItemTypeResult)
	catalogItem := result.CatalogItemType
	if catalogItem != nil {
		d.SetId(int64ToString(catalogItem.Id))
		d.Set("name", catalogItem.Name)
	} else {
		return diag.Errorf("Catalog item type not found in response data.") // should not happen
	}
	return diags
}
