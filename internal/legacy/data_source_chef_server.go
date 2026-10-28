package legacy

import (
	"context"
	"log"
	"strconv"

	sdk "github.com/gomorpheus/morpheus-go-sdk"
	"github.com/hashicorp/terraform-plugin-sdk/v2/diag"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
)

func dataSourceChefServer() *schema.Resource {
	return &schema.Resource{
		Description: "Provides a MTN Cloud chef server data source.",
		ReadContext: dataSourceChefServerRead,
		Schema: map[string]*schema.Schema{
			"id": {
				Type:          schema.TypeInt,
				Optional:      true,
				Computed:      true,
				ConflictsWith: []string{"name"},
			},
			"name": {
				Type:          schema.TypeString,
				Description:   "The name of the chef server",
				Optional:      true,
				ConflictsWith: []string{"id"},
			},
		},
	}
}

func dataSourceChefServerRead(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	client := meta.(*sdk.Client)

	// Warning or errors can be collected in a slice type
	var diags diag.Diagnostics

	name := d.Get("name").(string)
	value := d.Get("id").(int)

	// lookup by name if we do not have an value yet
	var resp *sdk.Response
	var err error

	resp, err = client.GetOptionSource("chefServer", &sdk.Request{})
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

	option, id, err := findOptionSourceOption(resp.Result.(*sdk.GetOptionSourceResult).Data, "Chef server", name, value)
	if err != nil {
		return diag.FromErr(err)
	}

	// store resource data
	d.SetId(strconv.FormatInt(id, 10))
	d.Set("id", id)
	d.Set("name", option.Name)

	return diags
}
