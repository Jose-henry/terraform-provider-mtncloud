package legacy

import (
	"context"
	"log"

	sdk "github.com/gomorpheus/morpheus-go-sdk"
	"github.com/hashicorp/terraform-plugin-sdk/v2/diag"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
)

func dataSourceWorkflow() *schema.Resource {
	return &schema.Resource{
		Description: "Provides a MTN Cloud workflow data source.",
		ReadContext: dataSourceWorkflowRead,
		Schema: map[string]*schema.Schema{
			"id": {
				Type:          schema.TypeInt,
				Optional:      true,
				ConflictsWith: []string{"name"},
				Computed:      true,
			},
			"name": {
				Type:          schema.TypeString,
				Description:   "The name of the workflow",
				Optional:      true,
				ConflictsWith: []string{"id"},
			},
		},
	}
}

func dataSourceWorkflowRead(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	client := meta.(*sdk.Client)

	// Warning or errors can be collected in a slice type
	var diags diag.Diagnostics

	id := d.Get("id").(int)
	name := d.Get("name").(string)

	// lookup by name if we do not have an id yet
	var resp *sdk.Response
	var err error
	if id == 0 && name != "" {
		resp, err = client.FindTaskSetByName(name)
	} else if id != 0 {
		resp, err = client.GetTaskSet(int64(id), &sdk.Request{})
	} else {
		return diag.Errorf("Workflow cannot be read without name or id")
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
	result := resp.Result.(*sdk.GetTaskSetResult)
	workflow := result.TaskSet
	if workflow != nil {
		d.SetId(int64ToString(workflow.ID))
		d.Set("name", workflow.Name)
	} else {
		return diag.Errorf("Workflow not found in response data.") // should not happen
	}
	return diags
}
