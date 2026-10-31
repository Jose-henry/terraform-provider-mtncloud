package legacy

import (
	"context"
	"fmt"
	"strconv"

	sdk "github.com/gomorpheus/morpheus-go-sdk"
	"github.com/hashicorp/terraform-plugin-sdk/v2/diag"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/validation"
)

func dataSourceVirtualImages() *schema.Resource {
	return &schema.Resource{
		Description: "Provides an MTN Cloud virtual images data source: lists the VM images you can see.",
		ReadContext: dataSourceVirtualImagesRead,
		Schema: map[string]*schema.Schema{
			"ids": {
				Type:        schema.TypeList,
				Description: "The IDs of the virtual images that match the filters.",
				Computed:    true,
				Elem:        &schema.Schema{Type: schema.TypeString},
			},
			"sort_ascending": {
				Type:        schema.TypeBool,
				Description: "Whether to sort the IDs in ascending order. Defaults to true",
				Default:     true,
				Optional:    true,
			},
			"source": {
				Type:         schema.TypeString,
				Description:  "The source of the MTN Cloud virtual image (User, System, Synced) (Default: User)",
				Optional:     true,
				Default:      "User",
				ValidateFunc: validation.StringInSlice([]string{"User", "System", "Synced"}, false),
			},
			"filter": {
				Type:        schema.TypeSet,
				Description: "Custom filter block as described below.",
				Optional:    true,
				Elem: &schema.Resource{
					Schema: map[string]*schema.Schema{
						"name": {
							Type:         schema.TypeString,
							Description:  "The name of the filter. Filter names are case-sensitive. Valid names are (name, type)",
							Required:     true,
							ValidateFunc: validation.StringInSlice([]string{"name", "type"}, false),
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

func dataSourceVirtualImagesRead(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	client := meta.(*sdk.Client)

	// Warning or errors can be collected in a slice type
	var diags diag.Diagnostics

	var sortOrder string
	var imageTypes []string
	var names []string

	if len(d.Get("filter").(*schema.Set).List()) > 0 {
		filters := d.Get("filter").(*schema.Set).List()
		for _, filter := range filters {
			filterData := filter.(map[string]interface{})
			if filterData["name"].(string) == "type" {
				for _, item := range filterData["values"].(*schema.Set).List() {
					imageTypes = append(imageTypes, item.(string))
				}
			}

			if filterData["name"].(string) == "name" {
				for _, item := range filterData["values"].(*schema.Set).List() {
					names = append(names, item.(string))
				}
			}
		}
	}

	if len(imageTypes) == 0 {
		imageTypes = append(imageTypes, "$")
	}

	if len(names) == 0 {
		names = append(names, "$")
	}

	// Sort virtual images in ascending or descending order
	if d.Get("sort_ascending").(bool) {
		sortOrder = "asc"
	} else {
		sortOrder = "desc"
	}

	output, err := ListAllVirtualImages(client, 200, sortOrder, d.Get("source").(string))
	if err != nil {
		return diag.FromErr(err)
	}

	var virtulImageIDs []string

	// store resource data
	for _, virtualImage := range output {
		if regexCheck(imageTypes, virtualImage.ImageType) && regexCheck(names, virtualImage.Name) {
			virtulImageIDs = append(virtulImageIDs, strconv.Itoa(int(virtualImage.ID)))
		}
	}

	d.SetId("1")
	d.Set("ids", virtulImageIDs)
	return diags
}

// ListAllVirtualImages pages through every virtual image. A failed page is an error, never a
// partial list read as complete.
func ListAllVirtualImages(client *sdk.Client, max int, sortOrder string, source string) ([]sdk.VirtualImage, error) {
	var images []sdk.VirtualImage
	for offset := 0; ; offset += max {
		params := map[string]string{
			"max":        strconv.Itoa(max),
			"offset":     strconv.Itoa(offset),
			"sort":       "id",
			"direction":  sortOrder,
			"filterType": source,
		}
		resp, err := client.ListVirtualImages(&sdk.Request{QueryParams: params})
		if err != nil {
			return nil, err
		}
		result, ok := resp.Result.(*sdk.ListVirtualImagesResult)
		if !ok || result.VirtualImages == nil {
			return nil, fmt.Errorf("listing virtual images: unexpected response")
		}
		images = append(images, *result.VirtualImages...)
		if len(*result.VirtualImages) < max || (result.Meta != nil && int64(len(images)) >= result.Meta.Total) {
			return images, nil
		}
	}
}
