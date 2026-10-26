package legacy

import (
	"context"
	"log"

	sdk "github.com/gomorpheus/morpheus-go-sdk"
	"github.com/hashicorp/terraform-plugin-sdk/v2/diag"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
)

func dataSourcePrice() *schema.Resource {
	return &schema.Resource{
		Description: "Master tenant only: customer (sub-tenant) logins get 403 from the API it uses. Looks up a price by name.",
		ReadContext: dataSourcePriceRead,
		Schema: map[string]*schema.Schema{
			"id": {
				Type:          schema.TypeInt,
				Optional:      true,
				ConflictsWith: []string{"name"},
				Computed:      true,
			},
			"name": {
				Type:          schema.TypeString,
				Description:   "The name of the MTN Cloud price",
				Optional:      true,
				ConflictsWith: []string{"id"},
			},
			"code": {
				Type:        schema.TypeString,
				Description: "The code of the MTN Cloud price",
				Computed:    true,
			},
		},
	}
}

func dataSourcePriceRead(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	client := meta.(*sdk.Client)

	// Warning or errors can be collected in a slice type
	var diags diag.Diagnostics

	name := d.Get("name").(string)
	id := d.Get("id").(int)

	// lookup by name if we do not have an id yet
	var resp *sdk.Response
	var err error
	if id == 0 && name != "" {
		resp, err = client.FindPriceByName(name)
	} else if id != 0 {
		resp, err = client.GetPrice(int64(id), &sdk.Request{})
	} else {
		return diag.Errorf("Price cannot be read without name or id")
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
	result := resp.Result.(*sdk.GetPriceResult)
	price := result.Price
	if price != nil {
		d.SetId(int64ToString(price.ID))
		d.Set("name", price.Name)
		d.Set("code", price.Code)
	} else {
		return diag.Errorf("Price not found in response data.") // should not happen
	}
	return diags
}
