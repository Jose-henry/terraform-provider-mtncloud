package legacy

import (
	"context"
	"log"

	sdk "github.com/gomorpheus/morpheus-go-sdk"
	"github.com/hashicorp/terraform-plugin-sdk/v2/diag"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
)

func dataSourceVirtualImage() *schema.Resource {
	return &schema.Resource{
		Description: "Provides a MTN Cloud virtual image data source.",
		ReadContext: dataSourceVirtualImageRead,
		Schema: map[string]*schema.Schema{
			"id": {
				Type:          schema.TypeInt,
				Optional:      true,
				ConflictsWith: []string{"name"},
				Computed:      true,
			},
			"name": {
				Type:          schema.TypeString,
				Description:   "The name of the MTN Cloud virtual image.",
				Optional:      true,
				ConflictsWith: []string{"id"},
			},
			"imagetype": {
				Type:          schema.TypeString,
				Description:   "The type of the MTN Cloud virtual image (alibaba,ami,azure-reference,digitalocean,oci,pxe,iso,qcow2,raw,vhd,vmdk).",
				Optional:      true,
				ConflictsWith: []string{"id"},
			},
		},
	}
}

func dataSourceVirtualImageRead(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	client := meta.(*sdk.Client)

	// Warning or errors can be collected in a slice type
	var diags diag.Diagnostics

	name := d.Get("name").(string)
	id := d.Get("id").(int)
	imagetype := d.Get("imagetype").(string)
	// lookup by name if we do not have an id yet
	var resp *sdk.Response
	var err error
	if id == 0 && name != "" && imagetype == "" {
		resp, err = client.FindVirtualImageByName(name)
	} else if id == 0 && name != "" && imagetype != "" {
		resp, err = client.FindVirtualImageByNameAndType(name, imagetype)
	} else if id != 0 {
		resp, err = client.GetVirtualImage(int64(id), &sdk.Request{})
	} else {
		return diag.Errorf("Virtual image cannot be read without name or id")
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
	result := resp.Result.(*sdk.GetVirtualImageResult)
	virtualImage := result.VirtualImage
	if virtualImage != nil {
		d.SetId(int64ToString(virtualImage.ID))
		d.Set("name", virtualImage.Name)
		d.Set("imagetype", virtualImage.ImageType)
	} else {
		return diag.Errorf("Virtual image not found in response data.") // should not happen
	}
	return diags
}
