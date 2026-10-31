package legacy

import (
	"context"
	"log"
	"strconv"

	sdk "github.com/gomorpheus/morpheus-go-sdk"
	"github.com/hashicorp/terraform-plugin-sdk/v2/diag"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
)

func dataSourceAnsibleTowerInventory() *schema.Resource {
	return &schema.Resource{
		Description: "Provides an MTN Cloud Ansible Tower inventory data source: looks up an inventory of an Ansible Tower integration by name or ID.",
		ReadContext: dataSourceAnsibleTowerInventoryRead,
		Schema: map[string]*schema.Schema{
			"id": {
				Type:          schema.TypeInt,
				Optional:      true,
				Computed:      true,
				ConflictsWith: []string{"name"},
			},
			"name": {
				Type:          schema.TypeString,
				Description:   "The name of the ansible tower inventory",
				Optional:      true,
				ConflictsWith: []string{"id"},
			},
		},
	}
}

func dataSourceAnsibleTowerInventoryRead(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	client := meta.(*sdk.Client)

	// Warning or errors can be collected in a slice type
	var diags diag.Diagnostics

	name := d.Get("name").(string)
	value := d.Get("id").(int)

	// lookup by name if we do not have an value yet
	var resp *sdk.Response
	var err error

	resp, err = client.GetOptionSource("ansibleTowerInventory", &sdk.Request{})
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

	option, id, err := findOptionSourceOption(resp.Result.(*sdk.GetOptionSourceResult).Data, "Ansible Tower inventory", name, value)
	if err != nil {
		return diag.FromErr(err)
	}

	// store resource data
	d.SetId(strconv.FormatInt(id, 10))
	d.Set("id", id)
	d.Set("name", option.Name)

	return diags
}
