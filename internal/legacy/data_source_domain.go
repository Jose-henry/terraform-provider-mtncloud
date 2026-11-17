package legacy

import (
	"context"
	"log"
	"net/http"
	"strings"

	sdk "github.com/gomorpheus/morpheus-go-sdk"
	"github.com/hashicorp/terraform-plugin-sdk/v2/diag"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
)

func dataSourceDomain() *schema.Resource {
	return &schema.Resource{
		Description: "Provides an MTN Cloud domain data source: looks up a network domain by name or ID.",
		ReadContext: dataSourceDomainRead,
		Schema: map[string]*schema.Schema{
			"id": {
				Type:          schema.TypeInt,
				Optional:      true,
				ConflictsWith: []string{"name"},
				Computed:      true,
			},
			"active": {
				Type:        schema.TypeBool,
				Description: "Whether the domain is active",
				Computed:    true,
			},
			"name": {
				Type:          schema.TypeString,
				Description:   "The name of the MTN Cloud domain",
				Optional:      true,
				ConflictsWith: []string{"id"},
			},
			"description": {
				Type:        schema.TypeString,
				Description: "The description of the MTN Cloud domain",
				Computed:    true,
			},
			"visibility": {
				Type:        schema.TypeString,
				Description: "The visibility of the MTN Cloud domain",
				Computed:    true,
			},
		},
	}
}

func dataSourceDomainRead(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	client := meta.(*sdk.Client)

	// Warning or errors can be collected in a slice type
	var diags diag.Diagnostics

	name := d.Get("name").(string)
	id := d.Get("id").(int)

	// A tenant's list shows domains shared from the master tenant (for example 1 localdomain, 16
	// example.com), but reading one of those by ID answers 404 (CLI, 2026-11-17; tester's fix 15). So
	// a name is matched in the list itself, and an ID the single read can't find is looked for there too.
	var domain *sdk.NetworkDomain
	switch {
	case id == 0 && name != "":
		list, nd := listDomains(client)
		if nd != nil {
			return nd
		}
		var found []sdk.NetworkDomain
		for _, x := range list {
			if strings.EqualFold(x.Name, name) {
				found = append(found, x)
			}
		}
		switch len(found) {
		case 0:
			return diag.Errorf("No domain is named %q. Check the name, or see data.mtncloud_domains for the domains you can use.", name)
		case 1:
			domain = &found[0]
		default:
			return diag.Errorf("%d domains are named %q: look this one up by id (see data.mtncloud_domains).", len(found), name)
		}
	case id != 0:
		resp, err := client.GetNetworkDomain(int64(id), &sdk.Request{})
		switch {
		case err == nil:
			domain = resp.Result.(*sdk.GetNetworkDomainResult).NetworkDomain
		case resp != nil && resp.StatusCode == http.StatusNotFound:
			list, nd := listDomains(client)
			if nd != nil {
				return nd
			}
			for i := range list {
				if list[i].ID == int64(id) {
					domain = &list[i]
				}
			}
			if domain == nil {
				return notFoundByID("domain", int64(id))
			}
		default:
			log.Printf("API FAILURE: %s - %v", resp, err)
			return diag.FromErr(err)
		}
	default:
		return diag.Errorf("Domain cannot be read without name or id")
	}

	if domain == nil {
		return diag.Errorf("Domain not found in response data.") // should not happen
	}
	d.SetId(int64ToString(domain.ID))
	setAttr(ctx, d, "active", domain.Active)
	setAttr(ctx, d, "name", domain.Name)
	setAttr(ctx, d, "description", domain.Description)
	setAttr(ctx, d, "visibility", domain.Visibility)
	return diags
}

// listDomains is every domain the login can use, shared ones included.
func listDomains(client *sdk.Client) ([]sdk.NetworkDomain, diag.Diagnostics) {
	resp, err := client.ListNetworkDomains(&sdk.Request{QueryParams: map[string]string{"max": "1000"}})
	if err != nil {
		log.Printf("API FAILURE: %s - %v", resp, err)
		return nil, diag.Errorf("Couldn't list the domains: %v", err)
	}
	list := resp.Result.(*sdk.ListNetworkDomainsResult).NetworkDomains
	if list == nil {
		return nil, nil
	}
	return *list, nil
}
