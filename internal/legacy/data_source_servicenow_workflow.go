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

func dataSourceServiceNowWorkflow() *schema.Resource {
	return &schema.Resource{
		Description: "Provides an MTN Cloud ServiceNow workflow data source: looks up a workflow of a ServiceNow integration by name or ID.",
		ReadContext: dataSourceServiceNowWorkflowRead,
		Schema: map[string]*schema.Schema{
			"id": {
				Type:          schema.TypeInt,
				Description:   "The ID of the ServiceNow integration",
				Optional:      true,
				ConflictsWith: []string{"name"},
				Computed:      true,
			},
			"name": {
				Type:          schema.TypeString,
				Description:   "The name of the ServiceNow workflow",
				Optional:      true,
				ConflictsWith: []string{"id"},
			},
			"integration_id": {
				Type:        schema.TypeInt,
				Description: "The ID of the ServiceNow integration",
				Required:    true,
			},
		},
	}
}

func dataSourceServiceNowWorkflowRead(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	client := meta.(*sdk.Client)

	// Warning or errors can be collected in a slice type
	var diags diag.Diagnostics

	name := d.Get("name").(string)

	resp, err := client.Execute(&sdk.Request{
		Method:      "GET",
		Path:        fmt.Sprintf("/api/options/deleteApprovalServiceNowWorkflows?config.accountIntegrationId=%d", d.Get("integration_id").(int)),
		QueryParams: map[string]string{},
	})
	if err != nil {
		log.Println("API ERROR: ", err)
	}
	log.Println("API RESPONSE:", resp)

	var itemResponsePayload CodeRepositories
	if err := json.Unmarshal(resp.Body, &itemResponsePayload); err != nil {
		return diag.Errorf("reading the ServiceNow workflows: %s", err)
	}
	foundWorkflow := false
	for _, v := range itemResponsePayload.Data {
		if v.Name == name {
			foundWorkflow = true
			d.SetId(intToString(v.Value))
		}
	}
	if !foundWorkflow {
		return diag.Errorf("Workflow named %s not found", name) // should not happen
	}
	return diags
}
