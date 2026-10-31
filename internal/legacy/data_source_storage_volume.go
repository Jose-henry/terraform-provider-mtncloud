package legacy

import (
	"context"
	"log"
	"net/http"

	sdk "github.com/gomorpheus/morpheus-go-sdk"
	"github.com/hashicorp/terraform-plugin-sdk/v2/diag"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
)

func dataSourceStorageVolume() *schema.Resource {
	return &schema.Resource{
		Description: "Provides an MTN Cloud storage volume data source: looks up a storage volume.",
		ReadContext: dataSourceStorageVolumeRead,
		Schema: map[string]*schema.Schema{
			"id": {
				Type:        schema.TypeInt,
				Description: "The ID of the storage volume",
				Required:    true,
			},
			"name": {
				Type:        schema.TypeString,
				Description: "The name of the storage volume",
				Computed:    true,
			},
			"active": {
				Type:        schema.TypeBool,
				Description: "Whether the storage volume is enabled or not",
				Computed:    true,
			},
			"category": {
				Type:        schema.TypeString,
				Description: "The storage volume category",
				Computed:    true,
			},
			"cloud_name": {
				Type:        schema.TypeString,
				Description: "The storage volume cloud name",
				Computed:    true,
			},
			"cloud_id": {
				Type:        schema.TypeInt,
				Description: "The storage volume cloud id",
				Computed:    true,
			},
			"datastore_name": {
				Type:        schema.TypeString,
				Description: "The storage volume datastore name",
				Computed:    true,
			},
			"datastore_id": {
				Type:        schema.TypeInt,
				Description: "The storage volume datastore id",
				Computed:    true,
			},
			"status": {
				Type:        schema.TypeString,
				Description: "The status of the storage volume",
				Computed:    true,
			},
			"source": {
				Type:        schema.TypeString,
				Description: "The associated cloud name",
				Computed:    true,
			},
			"type": {
				Type:        schema.TypeString,
				Description: "The storage volume type name",
				Computed:    true,
			},
			"type_id": {
				Type:        schema.TypeInt,
				Description: "The storage volume type id",
				Computed:    true,
			},
			"uuid": {
				Type:        schema.TypeString,
				Description: "The storage volume uuid",
				Computed:    true,
			},
		},
	}
}

func dataSourceStorageVolumeRead(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	client := meta.(*sdk.Client)

	// Warning or errors can be collected in a slice type
	var diags diag.Diagnostics

	id := d.Get("id").(int)

	// lookup by name if we do not have an id yet
	var resp *sdk.Response
	var err error

	resp, err = client.GetStorageVolume(int64(id), &sdk.Request{})
	if err != nil {
		if resp != nil && resp.StatusCode == http.StatusNotFound {
			return notFoundByID("storage volume", int64(id))
		} else {
			log.Printf("API FAILURE: %s - %v", resp, err)
			return diag.FromErr(err)
		}
	}
	log.Printf("API RESPONSE: %s", resp)

	// store resource data
	result := resp.Result.(*sdk.GetStorageVolumeResult)
	storageVolume := result.StorageVolume
	if storageVolume != nil {
		d.SetId(int64ToString(int64(storageVolume.ID.(float64))))
		setAttr(ctx, d, "name", storageVolume.Name)
		setAttr(ctx, d, "active", storageVolume.Active)
		setAttr(ctx, d, "category", storageVolume.Category)
		setAttr(ctx, d, "cloud_name", storageVolume.Zone.Name)
		setAttr(ctx, d, "cloud_id", storageVolume.Zone.ID)
		setAttr(ctx, d, "datastore_id", storageVolume.Datastore.ID)
		setAttr(ctx, d, "datastore_name", storageVolume.Datastore.Name)
		setAttr(ctx, d, "status", storageVolume.Status)
		setAttr(ctx, d, "source", storageVolume.Source)
		setAttr(ctx, d, "type", storageVolume.Type.Name)
		setAttr(ctx, d, "type_id", storageVolume.TypeId)
		setAttr(ctx, d, "uuid", storageVolume.Uuid)
	} else {
		return diag.Errorf("Storage volume not found in response data.") // should not happen
	}
	return diags
}
