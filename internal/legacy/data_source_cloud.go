package legacy

import (
	"context"
	"log"

	sdk "github.com/gomorpheus/morpheus-go-sdk"
	"github.com/hashicorp/terraform-plugin-sdk/v2/diag"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
)

func dataSourceCloud() *schema.Resource {
	return &schema.Resource{
		Description: "Master tenant only: customer (sub-tenant) logins get 403 from the API it uses. Looks up a cloud by name. Customers don't need it: the provider finds the MTN Cloud cloud (`MTNNG_CLOUD_AZ_1`) by itself.",
		ReadContext: dataSourceCloudRead,
		Schema: map[string]*schema.Schema{
			"id": {
				Type:          schema.TypeInt,
				Optional:      true,
				ConflictsWith: []string{"name"},
				Computed:      true,
			},
			"name": {
				Type:          schema.TypeString,
				Description:   "The name of the MTN Cloud cloud",
				Optional:      true,
				ConflictsWith: []string{"id"},
			},
			"code": {
				Type:        schema.TypeString,
				Description: "Optional code for use with policies",
				Computed:    true,
			},
			"location": {
				Type:        schema.TypeString,
				Description: "Optional location for your cloud",
				Computed:    true,
			},
			"external_id": {
				Type:        schema.TypeString,
				Description: "The external id of the cloud",
				Computed:    true,
			},
			"inventory_level": {
				Type:        schema.TypeString,
				Description: "The inventory level of the cloud",
				Computed:    true,
			},
			"guidance_mode": {
				Type:        schema.TypeString,
				Description: "The guidance mode of the cloud",
				Computed:    true,
			},
			"time_zone": {
				Type:        schema.TypeString,
				Description: "The time zone of the cloud",
				Computed:    true,
			},
			"costing_mode": {
				Type:        schema.TypeString,
				Description: "The costing mode of the cloud",
				Computed:    true,
			},
			"labels": {
				Type:        schema.TypeSet,
				Description: "The organization labels associated with the cloud",
				Computed:    true,
				Elem:        &schema.Schema{Type: schema.TypeString},
			},
			"group_ids": {
				Type:        schema.TypeSet,
				Description: "The ids of the groups granted access to the cloud",
				Computed:    true,
				Elem:        &schema.Schema{Type: schema.TypeInt},
			},
		},
	}
}

func dataSourceCloudRead(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	client := meta.(*sdk.Client)

	// Warning or errors can be collected in a slice type
	var diags diag.Diagnostics

	name := d.Get("name").(string)
	id := d.Get("id").(int)

	// lookup by name if we do not have an id yet
	var resp *sdk.Response
	var err error
	if id == 0 && name != "" {
		resp, err = client.FindCloudByName(name)
	} else if id != 0 {
		resp, err = client.GetCloud(int64(id), &sdk.Request{})
	} else {
		return diag.Errorf("Cloud cannot be read without name or id")
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
	result := resp.Result.(*sdk.GetCloudResult)
	cloud := result.Cloud
	if cloud != nil {
		d.SetId(int64ToString(cloud.ID))
		d.Set("name", cloud.Name)
		d.Set("code", cloud.Code)
		d.Set("location", cloud.Location)
		d.Set("external_id", cloud.ExternalID)
		d.Set("inventory_level", cloud.InventoryLevel)
		d.Set("guidance_mode", cloud.GuidanceMode)
		d.Set("time_zone", cloud.TimeZone)
		d.Set("costing_mode", cloud.CostingMode)
		d.Set("labels", cloud.Labels)
		var groupIds []int
		for _, group := range cloud.Groups {
			groupIds = append(groupIds, int(group.ID))
		}
		d.Set("group_ids", groupIds)
	} else {
		return diag.Errorf("Cloud not found in response data.") // should not happen
	}
	return diags
}
