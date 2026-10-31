package legacy

import (
	"context"
	"log"
	"strconv"

	sdk "github.com/gomorpheus/morpheus-go-sdk"
	"github.com/hashicorp/terraform-plugin-sdk/v2/diag"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/validation"
)

func dataSourceUserGroups() *schema.Resource {
	return &schema.Resource{
		Description: "Provides an MTN Cloud user groups data source: lists the user groups you can see.",
		ReadContext: dataSourceUserGroupsRead,
		Schema: map[string]*schema.Schema{
			"ids": {
				Type:        schema.TypeList,
				Description: "The IDs of the user groups that match the filters.",
				Computed:    true,
				Elem:        &schema.Schema{Type: schema.TypeString},
			},
			"sort_ascending": {
				Type:        schema.TypeBool,
				Description: "Whether to sort the IDs in ascending order. Defaults to true",
				Default:     true,
				Optional:    true,
			},
			"filter": {
				Type:        schema.TypeSet,
				Description: "Custom filter block as described below.",
				Optional:    true,
				Elem: &schema.Resource{
					Schema: map[string]*schema.Schema{
						"name": {
							Type:         schema.TypeString,
							Description:  "The name of the filter. Filter names are case-sensitive. Valid names are (name)",
							Required:     true,
							ValidateFunc: validation.StringInSlice([]string{"name"}, false),
						},
						"values": {
							Type:        schema.TypeSet,
							Description: "The filter values. Filter values are case-sensitive. Filters values support the use of Golang regex and can be tested at https://regex101.com/",
							Required:    true,
							Elem: &schema.Schema{
								Type: schema.TypeString,
							},
						},
					},
				},
			},
		},
	}
}

func dataSourceUserGroupsRead(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	client := meta.(*sdk.Client)

	// Warning or errors can be collected in a slice type
	var diags diag.Diagnostics

	var resp *sdk.Response
	var err error
	var sortOrder string
	var names []string

	if len(d.Get("filter").(*schema.Set).List()) > 0 {
		filters := d.Get("filter").(*schema.Set).List()
		for _, filter := range filters {
			filterDetails := filter.(map[string]interface{})
			if filterDetails["name"].(string) == "name" {
				for _, item := range filterDetails["values"].(*schema.Set).List() {
					names = append(names, item.(string))
				}
			}
		}
	}

	if len(names) == 0 {
		names = append(names, "$")
	}

	// Sort environments in ascending or descending order
	if d.Get("sort_ascending").(bool) {
		sortOrder = "asc"
	} else {
		sortOrder = "desc"
	}

	resp, err = client.ListUserGroups(&sdk.Request{
		QueryParams: map[string]string{
			"max":       "100",
			"sort":      "id",
			"direction": sortOrder,
		},
	})

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

	var userGroupIDs []string

	// store resource data
	result := resp.Result.(*sdk.ListUserGroupsResult)
	userGroups := result.UserGroups
	for _, userGroup := range *userGroups {
		if regexCheck(names, userGroup.Name) {
			userGroupIDs = append(userGroupIDs, strconv.Itoa(int(userGroup.ID)))
		}
	}
	d.SetId("1")
	d.Set("ids", userGroupIDs)
	return diags
}
