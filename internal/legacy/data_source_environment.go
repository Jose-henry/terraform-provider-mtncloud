package legacy

import (
	"context"
	"log"

	sdk "github.com/gomorpheus/morpheus-go-sdk"
	"github.com/hashicorp/terraform-plugin-sdk/v2/diag"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
)

func dataSourceEnvironment() *schema.Resource {
	return &schema.Resource{
		Description: "Provides an MTN Cloud environment data source: looks up an environment (such as Production) by name or ID.",
		ReadContext: dataSourceEnvironmentRead,
		Schema: map[string]*schema.Schema{
			"id": {
				Type:          schema.TypeInt,
				Optional:      true,
				ConflictsWith: []string{"name"},
				Computed:      true,
			},
			"active": {
				Type:        schema.TypeBool,
				Description: "Whether the environment is active",
				Computed:    true,
			},
			"name": {
				Type:          schema.TypeString,
				Description:   "The name of the MTN Cloud environment",
				Optional:      true,
				ConflictsWith: []string{"id"},
			},
			"description": {
				Type:        schema.TypeString,
				Description: "The description of the MTN Cloud environment",
				Computed:    true,
			},
			"code": {
				Type:        schema.TypeString,
				Description: "Optional code for use with policies",
				Computed:    true,
			},
			"visibility": {
				Type:        schema.TypeString,
				Description: "Whether the environment is visible in sub-tenants or not",
				Computed:    true,
			},
		},
	}
}

func dataSourceEnvironmentRead(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	client := meta.(*sdk.Client)

	// Warning or errors can be collected in a slice type
	var diags diag.Diagnostics

	name := d.Get("name").(string)
	id := d.Get("id").(int)

	// lookup by name if we do not have an id yet
	var resp *sdk.Response
	var err error
	if id == 0 && name != "" {
		resp, err = client.FindEnvironmentByName(name)
	} else if id != 0 {
		resp, err = client.GetEnvironment(int64(id), &sdk.Request{})
	} else {
		return diag.Errorf("Environment cannot be read without name or id")
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
	result := resp.Result.(*sdk.GetEnvironmentResult)
	environment := result.Environment
	if environment != nil {
		d.SetId(int64ToString(environment.ID))
		d.Set("active", environment.Active)
		d.Set("name", environment.Name)
		d.Set("code", environment.Code)
		d.Set("description", environment.Description)
		d.Set("visibility", environment.Visibility)
	} else {
		return diag.Errorf("Environment not found in response data.") // should not happen
	}
	return diags
}
