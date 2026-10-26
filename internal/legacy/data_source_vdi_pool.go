package legacy

import (
	"context"
	"log"

	sdk "github.com/gomorpheus/morpheus-go-sdk"
	"github.com/hashicorp/terraform-plugin-sdk/v2/diag"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
)

func dataSourceVDIPool() *schema.Resource {
	return &schema.Resource{
		Description: "Provides a MTN Cloud user group data source.",
		ReadContext: dataSourceVDIPoolRead,
		Schema: map[string]*schema.Schema{
			"id": {
				Type:          schema.TypeInt,
				Description:   "The ID of the vdi pool",
				Optional:      true,
				ConflictsWith: []string{"name"},
				Computed:      true,
			},
			"name": {
				Type:          schema.TypeString,
				Description:   "The name of the vdi pool",
				Optional:      true,
				ConflictsWith: []string{"id"},
			},
		},
	}
}

func dataSourceVDIPoolRead(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	client := meta.(*sdk.Client)

	// Warning or errors can be collected in a slice type
	var diags diag.Diagnostics

	name := d.Get("name").(string)
	id := d.Get("id").(int)

	// lookup by name if we do not have an id yet
	var resp *sdk.Response
	var err error
	if id == 0 && name != "" {
		resp, err = client.FindVDIPoolByName(name)
	} else if id != 0 {
		resp, err = client.GetVDIPool(int64(id), &sdk.Request{})
	} else {
		return diag.Errorf("VDI Pool cannot be read without name or id")
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
	result := resp.Result.(*sdk.GetVDIPoolResult)
	vdiPool := result.VDIPool
	if vdiPool != nil {
		d.SetId(int64ToString(vdiPool.ID))
		d.Set("name", vdiPool.Name)
	} else {
		return diag.Errorf("VDI Pool not found in response data.") // should not happen
	}
	return diags
}
