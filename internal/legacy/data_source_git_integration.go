package legacy

import (
	"context"
	"encoding/json"
	"fmt"
	"log"

	sdk "github.com/gomorpheus/morpheus-go-sdk"
	"github.com/hashicorp/terraform-plugin-sdk/v2/diag"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
)

func dataSourceGitIntegration() *schema.Resource {
	return &schema.Resource{
		Description: "Provides a MTN Cloud git integration data source.",
		ReadContext: dataSourceGitIntegrationRead,
		Schema: map[string]*schema.Schema{
			"id": {
				Type:          schema.TypeInt,
				Description:   "The ID of the MTN Cloud git integration",
				Optional:      true,
				ConflictsWith: []string{"name"},
				Computed:      true,
			},
			"name": {
				Type:          schema.TypeString,
				Description:   "The name of the MTN Cloud git integration.",
				Optional:      true,
				ConflictsWith: []string{"id"},
			},
			"repository_ids": {
				Computed:    true,
				Type:        schema.TypeMap,
				Description: "A map of git repository ids for use with integrations that reference a git repository",
				Elem:        &schema.Schema{Type: schema.TypeInt},
			},
		},
	}
}

func dataSourceGitIntegrationRead(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	client := meta.(*sdk.Client)

	// Warning or errors can be collected in a slice type
	var diags diag.Diagnostics

	name := d.Get("name").(string)
	id := d.Get("id").(int)

	// lookup by name if we do not have an id yet
	var resp *sdk.Response
	var err error
	if id == 0 && name != "" {
		resp, err = client.FindIntegrationByName(name)
	} else if id != 0 {
		resp, err = client.GetIntegration(int64(id), &sdk.Request{})
	} else {
		return diag.Errorf("Integration cannot be read without name or id")
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
	result := resp.Result.(*sdk.GetIntegrationResult)
	integration := result.Integration
	if integration != nil {
		d.SetId(int64ToString(integration.ID))
		d.Set("name", integration.Name)
		resp, err = client.Execute(&sdk.Request{
			Method:      "GET",
			Path:        fmt.Sprintf("/api/options/codeRepositories?integrationId=%d", integration.ID),
			QueryParams: map[string]string{},
		})
		if err != nil {
			log.Println("API ERROR: ", err)
		}
		log.Println("API RESPONSE:", resp)
		repo_ids := make(map[string]int)

		var itemResponsePayload CodeRepositories
		json.Unmarshal(resp.Body, &itemResponsePayload)
		for _, v := range itemResponsePayload.Data {
			repo_ids[v.Name] = v.Value
		}
		d.Set("repository_ids", repo_ids)
	} else {
		return diag.Errorf("Git integration not found in response data.") // should not happen
	}

	return diags
}
