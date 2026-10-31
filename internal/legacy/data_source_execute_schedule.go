package legacy

import (
	"context"
	"log"
	"net/http"

	sdk "github.com/gomorpheus/morpheus-go-sdk"
	"github.com/hashicorp/terraform-plugin-sdk/v2/diag"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
)

func dataSourceExecuteSchedule() *schema.Resource {
	return &schema.Resource{
		Description: "Provides an MTN Cloud execution schedule data source: looks up an execution schedule by name or ID.",
		ReadContext: dataSourceExecuteScheduleRead,
		Schema: map[string]*schema.Schema{
			"id": {
				Type:          schema.TypeInt,
				Optional:      true,
				ConflictsWith: []string{"name"},
				Computed:      true,
			},
			"name": {
				Type:          schema.TypeString,
				Description:   "The name of the execute schedule",
				Optional:      true,
				ConflictsWith: []string{"id"},
			},
		},
	}
}

func dataSourceExecuteScheduleRead(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	client := meta.(*sdk.Client)

	// Warning or errors can be collected in a slice type
	var diags diag.Diagnostics

	name := d.Get("name").(string)
	id := d.Get("id").(int)

	// lookup by name if we do not have an id yet
	var resp *sdk.Response
	var err error
	if id == 0 && name != "" {
		resp, err = client.FindExecuteScheduleByName(name)
	} else if id != 0 {
		resp, err = client.GetExecuteSchedule(int64(id), &sdk.Request{})
	} else {
		return diag.Errorf("Execute schedule cannot be read without name or id")
	}
	if err != nil {
		if id == 0 {
			if nd := nameLookupError(client, err, "execution schedule", name, sdk.ExecuteSchedulesPath); nd != nil {
				return nd
			}
		}
		if resp != nil && resp.StatusCode == http.StatusNotFound && id != 0 {
			return notFoundByID("execution schedule", int64(id))
		} else {
			log.Printf("API FAILURE: %s - %v", resp, err)
			return diag.FromErr(err)
		}
	}
	log.Printf("API RESPONSE: %s", resp)

	// store resource data
	result := resp.Result.(*sdk.GetExecuteScheduleResult)
	executeSchedule := result.ExecuteSchedule
	if executeSchedule != nil {
		d.SetId(int64ToString(executeSchedule.ID))
		setAttr(ctx, d, "name", executeSchedule.Name)
	} else {
		return diag.Errorf("Execute schedule not found in response data.") // should not happen
	}
	return diags
}
