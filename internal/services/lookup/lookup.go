// Package lookup holds the catalog data sources: read-only lists that answer "what can I choose
// from?" (projects, images, plans, datastores, security groups, external networks, catalog
// items) so customers never copy an ID from the console (D-006, dev-docs/catalog-lookups.md).
//
// Every lookup uses the endpoints the console's provisioning wizard uses, which a customer role
// is allowed to call. Results are sorted, so plans never show a diff from API ordering.
package lookup

import (
	"context"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/mtn/terraform-provider-mtncloud/internal/client"
	"github.com/mtn/terraform-provider-mtncloud/internal/validators"
)

// DataSources lists every data source in this package, for the provider to register.
func DataSources() []func() datasource.DataSource {
	return append([]func() datasource.DataSource{
		newProjects,
		newImages,
		newPlans,
		newDatastores,
		newSecurityGroups,
		newExternalNetworks,
		newCatalogItems,
		newRouters,
		newInstances,
		newInstance,
		newNetwork,
		newProject,
		newRouter,
		newCypher,
		newCatalogItem,
		newImage,
		newFloatingIPs,
		newNetworks,
	}, recordDataSources()...)
}

// base gives every lookup its client. Embed it and the data source gets Configure for free.
type base struct {
	client *client.Client
}

func (b *base) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	if req.ProviderData == nil {
		return // provider not configured yet (validate); Read runs only after Configure
	}
	c, ok := req.ProviderData.(*client.Client)
	if !ok {
		resp.Diagnostics.AddError("Unexpected provider data", fmt.Sprintf("expected *client.Client, got %T. This is a provider bug.", req.ProviderData))
		return
	}
	b.client = c
}

// ready reports whether the provider is configured. It isn't when the provider block uses values
// only known after apply; Terraform may still ask a data source to read then, and it must fail
// with an explanation, not crash on a nil client.
func (b *base) ready(diags *diag.Diagnostics) bool {
	if b.client == nil {
		diags.AddError("MTN Cloud provider is not configured yet",
			"The provider block uses values that are only known after apply (for example a resource attribute), so this data source "+
				"can't be read during plan. Use values known at plan time (variables, environment variables), or apply the resources they come from first (terraform apply -target=...).")
		return false
	}
	return true
}

func nameRegexAttribute(what string) schema.StringAttribute {
	return schema.StringAttribute{
		Optional:    true,
		Description: fmt.Sprintf("Only return %s whose name matches this regular expression (RE2 syntax), for example `^web-`.", what),
		Validators:  []validator.String{validators.Regex()},
	}
}

func projectAttribute() schema.StringAttribute {
	return schema.StringAttribute{
		Required:    true,
		Description: "Project name, as shown in the console (for example `web-prod`). The full platform name, with the tenant suffix, also works.",
	}
}

// matcher returns a name filter for an optional regex. The regex was validated at plan time.
func matcher(re types.String) (func(string) bool, diag.Diagnostics) {
	var diags diag.Diagnostics
	if re.IsNull() || re.ValueString() == "" {
		return func(string) bool { return true }, diags
	}
	r, err := regexp.Compile(re.ValueString())
	if err != nil {
		diags.AddError("Invalid name_regex", err.Error())
		return nil, diags
	}
	return r.MatchString, diags
}

// textMatches reports whether a field passes an optional exact filter (case ignored).
func textMatches(have string, want types.String) bool {
	return want.IsNull() || strings.EqualFold(have, want.ValueString())
}

// labelsMatch reports whether every wanted label is among have (case ignored).
func labelsMatch(have []string, want []types.String) bool {
	for _, w := range want {
		if !slices.ContainsFunc(have, func(h string) bool { return strings.EqualFold(h, w.ValueString()) }) {
			return false
		}
	}
	return true
}

// tagsMatch reports whether every wanted tag is in have with the same value.
func tagsMatch(have map[string]string, want map[string]types.String) bool {
	for k, v := range want {
		if got, ok := have[k]; !ok || got != v.ValueString() {
			return false
		}
	}
	return true
}

// projectError reports a failed project lookup: a name that matched nothing on the project
// attribute, anything else (the API or the network failing) as an API error.
func projectError(diags *diag.Diagnostics, err error) {
	if client.IsNoMatch(err) {
		diags.AddAttributeError(path.Root("project"), "Unknown project", err.Error())
		return
	}
	apiError(diags, "projects", err)
}

// apiError reports a client error as a diagnostic, naming what was being looked up.
func apiError(diags *diag.Diagnostics, what string, err error) {
	diags.AddError(fmt.Sprintf("Unable to read %s", what), err.Error())
}

// nonNil turns a nil map into an empty one, so state holds {} rather than null.
func nonNil(m map[string]string) map[string]string {
	if m == nil {
		return map[string]string{}
	}
	return m
}
