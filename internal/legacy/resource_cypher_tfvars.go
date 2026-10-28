package legacy

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"log"

	sdk "github.com/gomorpheus/morpheus-go-sdk"
	"github.com/hashicorp/terraform-plugin-sdk/v2/diag"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
)

func resourceCypherTFVars() *schema.Resource {
	return &schema.Resource{
		Description:   "Provides a MTN Cloud cypher tfvars secret resource.",
		CreateContext: resourceCypherTFVarsCreate,
		ReadContext:   resourceCypherTFVarsRead,
		DeleteContext: resourceCypherTFVarsDelete,

		Schema: map[string]*schema.Schema{
			"id": {
				Type:        schema.TypeString,
				Description: "The ID of the cypher tfvars secret",
				Computed:    true,
			},
			"key": {
				Type:        schema.TypeString,
				Description: "The path of the cypher tfvars secret, excluding the secret prefix",
				Required:    true,
				ForceNew:    true,
			},
			"value": {
				Type:        schema.TypeString,
				Description: "The value of the cypher tfvars secret",
				Required:    true,
				Sensitive:   true,
				ForceNew:    true,
			},
			"ttl": {
				Type:        schema.TypeInt,
				Description: "The time to live of the cypher tfvars secret",
				Optional:    true,
				Computed:    true,
				ForceNew:    true,
			},
		},
		Importer: &schema.ResourceImporter{
			StateContext: cypherImporter("tfvars"),
		},
	}
}

func resourceCypherTFVarsCreate(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	client := meta.(*sdk.Client)

	// Warning or errors can be collected in a slice type
	var diags diag.Diagnostics

	req := &sdk.Request{
		Body: map[string]interface{}{
			"value": d.Get("value").(string),
		},
		QueryParams: map[string]string{
			"ttl":  strconv.Itoa(d.Get("ttl").(int)),
			"type": "string",
		},
	}

	tfvarsPath := fmt.Sprintf("tfvars/%s", d.Get("key").(string))
	resp, err := client.CreateCypher(tfvarsPath, req)
	if err != nil {
		log.Printf("API FAILURE: %s - %s", resp, err)
		return diag.FromErr(err)
	}
	// Masking to avoid credential exposure
	//log.Printf("API RESPONSE: %s", resp)

	result := resp.Result.(*sdk.CreateCypherResult)
	// Successfully created resource, now set id
	d.SetId(int64ToString(result.Cypher.ID))

	resourceCypherTFVarsRead(ctx, d, meta)
	return diags
}

func resourceCypherTFVarsRead(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	client := meta.(*sdk.Client)
	// Warning or errors can be collected in a slice type
	var diags diag.Diagnostics

	id := d.Id()

	var resp *sdk.Response
	var err error
	if id != "" {
		tfvarsPath := fmt.Sprintf("tfvars/%s", d.Get("key").(string))
		resp, err = client.GetCypher(tfvarsPath, &sdk.Request{})
	} else {
		return diag.Errorf("Cypher cannot be read without id")
	}

	if err != nil {
		if resp != nil && resp.StatusCode == 404 {
			log.Printf("API 404: %s - %s", resp, err)
			log.Printf("Forcing recreation of resource")
			d.SetId("")
			return diags
		} else {
			log.Printf("API FAILURE: %s - %s", resp, err)
			return diag.FromErr(err)
		}
	}
	// Masking to avoid credential exposure
	//log.Printf("API RESPONSE: %s", resp)

	// store resource data
	result := resp.Result.(*sdk.GetCypherResult)
	if result.Cypher != nil {
		d.SetId(int64ToString(result.Cypher.ID))
		keyData := strings.Split(result.Cypher.ItemKey, "/")
		keyData = keyData[1:]
		d.Set("key", strings.Join(keyData, "/"))
		d.Set("ttl", result.LeaseDuration)
		if v, ok := result.Data.(string); ok {
			d.Set("value", v) // the stored value: an import adopts it, and a change made elsewhere shows
		}
	} else {
		return diag.Errorf("read operation: cypher not found in response data") // should not happen
	}

	return diags
}

func resourceCypherTFVarsDelete(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	client := meta.(*sdk.Client)

	// Warning or errors can be collected in a slice type
	var diags diag.Diagnostics

	req := &sdk.Request{}
	tfvarsPath := fmt.Sprintf("tfvars/%s", d.Get("key").(string))
	resp, err := client.DeleteCypher(tfvarsPath, req)
	if err != nil {
		if resp != nil && resp.StatusCode == 404 {
			log.Printf("API 404: %s - %s", resp, err)
			return diag.FromErr(err)
		} else {
			log.Printf("API FAILURE: %s - %s", resp, err)
			return diag.FromErr(err)
		}
	}
	log.Printf("API RESPONSE: %s", resp)
	d.SetId("")
	return diags
}
