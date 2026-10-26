package legacy

import (
	"context"
	"fmt"
	"log"
	"strings"

	sdk "github.com/gomorpheus/morpheus-go-sdk"
	"github.com/hashicorp/terraform-plugin-sdk/v2/diag"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
)

func dataSourceAnsibleTowerInventory() *schema.Resource {
	return &schema.Resource{
		Description: "Provides a MTN Cloud ansible tower inventory data source.",
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
	value := d.Get("id")

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

	var inventory sdk.OptionSourceOption
	allInventories := *resp.Result.(*sdk.GetOptionSourceResult).Data
	for i := range allInventories {
		if value == 0 && name != "" {
			if strings.EqualFold(allInventories[i].Name, name) {
				inventory = allInventories[i]
				break
			}
		} else if value != 0 {
			if value == allInventories[i].Value {
				inventory = allInventories[i]
				break
			}
		} else {
			return diag.Errorf("Ansible tower inventory cannot be read without name or value")
		}
	}

	// store resource data
	d.SetId(fmt.Sprintf("%g", inventory.Value.(float64)))
	d.Set("id", inventory.Value)
	d.Set("name", inventory.Name)

	return diags
}
