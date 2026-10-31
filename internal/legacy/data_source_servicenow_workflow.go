package legacy

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"

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
		return diag.Errorf("listing the integration's ServiceNow workflows: %s", err)
	}
	log.Println("API RESPONSE:", resp)

	var itemResponsePayload CodeRepositories
	if err := json.Unmarshal(resp.Body, &itemResponsePayload); err != nil {
		return diag.Errorf("reading the ServiceNow workflows: %s", err)
	}
	objs := make([]namedObject, 0, len(itemResponsePayload.Data))
	var match []namedObject
	for _, v := range itemResponsePayload.Data {
		o := namedObject{ID: int64(v.Value), Name: v.Name}
		objs = append(objs, o)
		if strings.EqualFold(v.Name, name) { // as the API matches names elsewhere
			match = append(match, o)
		}
	}
	if len(match) != 1 {
		return diag.Errorf("%s", nameLookupMessage("ServiceNow workflow", name, objs, true))
	}
	d.SetId(int64ToString(match[0].ID))
	return diags
}
