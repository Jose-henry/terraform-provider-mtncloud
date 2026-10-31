package legacy

import (
	"context"

	sdk "github.com/gomorpheus/morpheus-go-sdk"
	"github.com/hashicorp/terraform-plugin-sdk/v2/diag"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
)

func dataSourceKeyPair() *schema.Resource {
	return &schema.Resource{
		Description: "Provides an MTN Cloud key pair data source: looks up an SSH key pair by name or ID.",
		ReadContext: dataSourceMorphesKeyPairRead,
		Schema: map[string]*schema.Schema{
			"id": {
				Type:          schema.TypeInt,
				Description:   "The ID of the key pair",
				Optional:      true,
				ConflictsWith: []string{"name"},
				Computed:      true,
			},
			"name": {
				Type:          schema.TypeString,
				Description:   "The name of the integration",
				Optional:      true,
				ConflictsWith: []string{"id"},
			},
			"publickey": {
				Type:        schema.TypeString,
				Description: "PublicKey of the KeyPair",
				Optional:    true,
			},
		},
	}
}

func dataSourceMorphesKeyPairRead(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	client := meta.(*sdk.Client)

	// Warning or errors can be collected in a slice type
	var diags diag.Diagnostics
	//read name and ID from Terraform
	name := d.Get("name").(string)
	id := toInt64(d.Id())

	var resp *sdk.Response
	var err error
	if (id != 0 && name == "") || (id != 0 && name != "") { //if an ID is defined, use ID to retrieve KeyPair
		resp, err = client.GetKeyPair(id)
	} else if id == 0 && name != "" { // if no ID is defined search by name
		resp, err = client.GetKeyPairByName(name)
	} else if id == 0 && name == "" { // in case neither ID nore name is defined throw an error
		return diag.Errorf("Key pair cannot be read without name or id")
	}

	if err != nil {
		return diag.FromErr(err)
	}
	var keyPair *sdk.KeyPair
	if id != 0 {
		result := resp.Result.(*sdk.GetKeyPairResult) //read KeyPair from response, retireving an KeyPair via ID returns a pointer of type KeyPair, find by name return an pointer to an Array of KeyPair
		keyPair = result.KeyPair
	} else if name != "" {
		listResult := resp.Result.(*sdk.ListKeyPairsResult)
		keyPairs := listResult.KeyPairs
		keyPair = &(*keyPairs)[0]
	}
	if keyPair != nil {
		d.SetId(int64ToString((*keyPair).ID))
		d.Set("name", (*keyPair).Name)
		d.Set("publickey", (*keyPair).PublicKey)
	} else {
		return diag.Errorf("Key pair not found in response data.") // should not happen
	}
	return diags
}
