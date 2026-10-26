package legacy

import (
	"context"
	"log"
	"strconv"

	sdk "github.com/gomorpheus/morpheus-go-sdk"
	"github.com/hashicorp/terraform-plugin-sdk/v2/diag"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
)

func dataSourceCloudDatastore() *schema.Resource {
	return &schema.Resource{
		Description: "Provides a MTN Cloud cloud datastore data source.",
		ReadContext: dataSourceCloudDatastoreRead,
		Schema: map[string]*schema.Schema{
			"cloud_id": {
				Type:        schema.TypeInt,
				Description: "The id of the MTN Cloud cloud to search for the datastore.",
				Required:    true,
			},
			"id": {
				Type:          schema.TypeInt,
				Description:   "The ID of the cloud datastore",
				Optional:      true,
				ConflictsWith: []string{"name"},
				Computed:      true,
			},
			"name": {
				Type:          schema.TypeString,
				Description:   "The name of the MTN Cloud cloud datastore.",
				Optional:      true,
				ConflictsWith: []string{"id"},
			},
			"active": {
				Type:        schema.TypeBool,
				Description: "Whether the cloud datastore is enabled or not",
				Computed:    true,
			},
			"type": {
				Type:        schema.TypeString,
				Description: "The cloud datastore type",
				Computed:    true,
			},
			"visibility": {
				Type:        schema.TypeString,
				Description: "The cloud datastore visibility",
				Computed:    true,
			},
			"tenants": {
				Type:     schema.TypeList,
				Computed: true,
				Elem: &schema.Resource{
					Schema: map[string]*schema.Schema{
						"id": {
							Type:        schema.TypeString,
							Description: "The id of the tenant",
							Optional:    true,
						},
						"name": {
							Type:        schema.TypeString,
							Description: "The name of the tenant",
							Computed:    true,
						},
						"default_store": {
							Type:        schema.TypeBool,
							Description: "Whether the datastore is the default datastore for the cloud",
							Computed:    true,
						},
						"default_target": {
							Type:        schema.TypeBool,
							Description: "Whether the datastore is the default datastore when uploading virtual images",
							Computed:    true,
						},
					},
				},
			},
		},
	}
}

func dataSourceCloudDatastoreRead(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	client := meta.(*sdk.Client)

	// Warning or errors can be collected in a slice type
	var diags diag.Diagnostics

	name := d.Get("name").(string)
	id := d.Get("id").(int)
	cloudId := d.Get("cloud_id").(int)

	// lookup by name if we do not have an id yet
	var resp *sdk.Response
	var err error
	if id == 0 && name != "" {
		resp, err = client.ListCloudDatastores(int64(cloudId), &sdk.Request{
			QueryParams: map[string]string{
				"name": name,
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
		result := resp.Result.(*sdk.ListCloudDatastoresResult)
		datastoreCount := len(*result.Datastores)
		if datastoreCount != 1 {
			return diag.Errorf("found %d datastores for %v", datastoreCount, name)
		}
		firstRecord := (*result.Datastores)[0]
		datastoreId := firstRecord.ID
		resp, err = client.GetCloudDatastore(int64(cloudId), datastoreId, &sdk.Request{})
	} else if id != 0 {
		resp, err = client.GetCloudDatastore(int64(cloudId), int64(id), &sdk.Request{})
	} else {
		return diag.Errorf("Cloud datastore cannot be read without name or id")
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
	result := resp.Result.(*sdk.GetCloudDatastoreResult)
	datastore := result.Datastore
	if datastore != nil {
		d.SetId(int64ToString(datastore.ID))
		d.Set("name", datastore.Name)
		d.Set("active", datastore.Active)
		d.Set("type", datastore.Type)
		d.Set("visibility", datastore.Visibility)
		var tenants []map[string]interface{}
		for _, tenant := range datastore.Tenants {
			row := make(map[string]interface{})
			row["id"] = strconv.Itoa(tenant.ID)
			row["name"] = tenant.Name
			row["default_store"] = tenant.DefaultStore
			row["default_target"] = tenant.DefaultTarget
			tenants = append(tenants, row)
		}
		d.Set("tenants", tenants)
	} else {
		return diag.Errorf("Cloud datastore not found in response data.") // should not happen
	}
	return diags
}
