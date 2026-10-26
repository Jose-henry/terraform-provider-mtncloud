package legacy

import (
	"context"
	"log"
	"strconv"

	sdk "github.com/gomorpheus/morpheus-go-sdk"
	"github.com/hashicorp/terraform-plugin-sdk/v2/diag"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
)

func dataSourcePlan() *schema.Resource {
	return &schema.Resource{
		Description: "Master tenant only: customer (sub-tenant) logins get 403 from the API it uses. Looks up a service plan by name. Customers: use `mtncloud_plans`.",
		ReadContext: dataSourcePlanRead,
		Schema: map[string]*schema.Schema{
			"id": {
				Type:          schema.TypeInt,
				Optional:      true,
				ConflictsWith: []string{"name"},
				Computed:      true,
			},
			"name": {
				Type:          schema.TypeString,
				Description:   "The name of the MTN Cloud plan.",
				Optional:      true,
				ConflictsWith: []string{"id"},
			},
			"code": {
				Type:        schema.TypeString,
				Description: "Optional code for use with policies",
				Computed:    true,
			},
			"description": {
				Type:        schema.TypeString,
				Description: "The description of the plan",
				Computed:    true,
			},
			"provision_type": {
				Type:        schema.TypeString,
				Description: "The name of the service plan provisiong type (i.e. - Amazon EC2, Azure, Google, Nutanix, VMware, etc.)",
				Required:    true,
			},
		},
	}
}

func dataSourcePlanRead(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	client := meta.(*sdk.Client)

	// Warning or errors can be collected in a slice type
	var diags diag.Diagnostics

	name := d.Get("name").(string)
	id := d.Get("id").(int)

	// lookup by name if we do not have an id yet
	var resp *sdk.Response
	var err error
	if id == 0 && name != "" {

		// Find the provision type to filter service plans
		resp, err = client.FindProvisionTypeByName(d.Get("provision_type").(string))
		if err != nil {
			log.Printf("API FAILURE: %s - %v", resp, err)
			return diag.FromErr(err)
		}
		provisionType := resp.Result.(*sdk.GetProvisionTypeResult)

		// Find the service plan by name and provision type
		listResp, err := client.ListPlans(&sdk.Request{
			QueryParams: map[string]string{
				"name":            d.Get("name").(string),
				"provisionTypeId": strconv.Itoa(int(provisionType.ProvisionType.ID)),
			},
		})
		if err != nil {
			log.Printf("API FAILURE: %s - %v", resp, err)
			return diag.FromErr(err)
		}

		listPlan := listResp.Result.(*sdk.ListPlansResult)
		plan := (*listPlan.Plans)[0]

		resp, err = client.GetPlan(int64(plan.ID), &sdk.Request{})
		if err != nil {
			log.Printf("API FAILURE: %s - %v", resp, err)
			return diag.FromErr(err)
		}
	} else if id != 0 {
		resp, err = client.GetPlan(int64(id), &sdk.Request{})
	} else {
		return diag.Errorf("Plan cannot be read without name or id")
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
	result := resp.Result.(*sdk.GetPlanResult)
	plan := result.Plan
	if plan != nil {
		d.SetId(int64ToString(plan.ID))
		d.Set("name", plan.Name)
		d.Set("code", plan.Code)
		d.Set("description", plan.Description)
	} else {
		return diag.Errorf("Plan not found in response data.") // should not happen
	}
	return diags
}
