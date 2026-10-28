package legacy

import (
	"context"
	"log"
	"strconv"

	sdk "github.com/gomorpheus/morpheus-go-sdk"
	"github.com/hashicorp/terraform-plugin-sdk/v2/diag"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
)

func dataSourceVrealizeOrchestratorWorkflow() *schema.Resource {
	return &schema.Resource{
		Description: "Provides a MTN Cloud vRO workflow data source.",
		ReadContext: dataSourceVrealizeOrchestratorWorkflowRead,
		Schema: map[string]*schema.Schema{
			"value": {
				Type:          schema.TypeInt,
				Optional:      true,
				Computed:      true,
				ConflictsWith: []string{"name"},
			},
			"name": {
				Type:          schema.TypeString,
				Description:   "The name of the vRealize orchestrator workflow",
				Optional:      true,
				ConflictsWith: []string{"value"},
			},
		},
	}
}

func dataSourceVrealizeOrchestratorWorkflowRead(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	client := meta.(*sdk.Client)

	// Warning or errors can be collected in a slice type
	var diags diag.Diagnostics

	name := d.Get("name").(string)
	value := d.Get("value").(int)

	// lookup by name if we do not have an value yet
	var resp *sdk.Response
	var err error

	resp, err = client.GetOptionSource("vroWorkflow", &sdk.Request{})
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

	option, id, err := findOptionSourceOption(resp.Result.(*sdk.GetOptionSourceResult).Data, "vRO workflow", name, value)
	if err != nil {
		return diag.FromErr(err)
	}

	// store resource data
	d.SetId(strconv.FormatInt(id, 10))
	d.Set("value", id)
	d.Set("name", option.Name)

	return diags
}
