package legacy

import (
	"context"
	"log"
	"net/http"

	sdk "github.com/gomorpheus/morpheus-go-sdk"
	"github.com/hashicorp/terraform-plugin-sdk/v2/diag"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
)

func dataSourceTask() *schema.Resource {
	return &schema.Resource{
		Description: "Provides an MTN Cloud task data source: looks up a task by name or ID.",
		ReadContext: dataSourceTaskRead,
		Schema: map[string]*schema.Schema{
			"id": {
				Type:          schema.TypeInt,
				Optional:      true,
				ConflictsWith: []string{"name"},
				Computed:      true,
			},
			"name": {
				Type:          schema.TypeString,
				Description:   "The name of the task",
				Optional:      true,
				ConflictsWith: []string{"id"},
			},
		},
	}
}

func dataSourceTaskRead(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	client := meta.(*sdk.Client)

	// Warning or errors can be collected in a slice type
	var diags diag.Diagnostics

	name := d.Get("name").(string)
	id := d.Get("id").(int)

	// lookup by name if we do not have an id yet
	var resp *sdk.Response
	var err error
	if id == 0 && name != "" {
		resp, err = client.FindTaskByName(name)
	} else if id != 0 {
		resp, err = client.GetTask(int64(id), &sdk.Request{})
	} else {
		return diag.Errorf("Task cannot be read without name or id")
	}
	if err != nil {
		if id == 0 {
			if nd := nameLookupError(client, err, "task", name, sdk.TasksPath); nd != nil {
				return nd
			}
		}
		if resp != nil && resp.StatusCode == http.StatusNotFound && id != 0 {
			return notFoundByID("task", int64(id))
		} else {
			log.Printf("API FAILURE: %s - %v", resp, err)
			return diag.FromErr(err)
		}
	}
	log.Printf("API RESPONSE: %s", resp)

	// store resource data
	result := resp.Result.(*sdk.GetTaskResult)
	task := result.Task
	if task != nil {
		d.SetId(int64ToString(task.ID))
		setAttr(ctx, d, "name", task.Name)
	} else {
		return diag.Errorf("Task not found in response data.") // should not happen
	}
	return diags
}
