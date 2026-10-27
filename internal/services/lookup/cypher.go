package lookup

import (
	"context"
	"fmt"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/mtn/terraform-provider-mtncloud/internal/validators"
)

func newCypher() datasource.DataSource { return &cypherDataSource{} }

// cypherDataSource is mtncloud_cypher: one Cypher key's value. It only reads keys that exist:
// reading a missing password/, uuid/ or key/ key would create it.
type cypherDataSource struct{ base }

type cypherModel struct {
	Key          types.String `tfsdk:"key"`
	Value        types.String `tfsdk:"value"`
	Values       types.Map    `tfsdk:"values"`
	LeaseTimeout types.Int64  `tfsdk:"lease_timeout"`
	ExpireDate   types.String `tfsdk:"expire_date"`
}

func (d *cypherDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_cypher"
}

func (d *cypherDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Reads one key from Cypher, MTN Cloud's secret store, on any mount: `secret/`, `tfvars/`, `password/`, `uuid/`, " +
			"`key/` or `vault/`. The key must exist: this data source never creates one (reading a missing `password/`, `uuid/` or " +
			"`key/` key in the console creates it). The value is stored in Terraform state, marked sensitive: protect your state. " +
			"To list keys without their values, use `mtncloud_cyphers`.",
		Attributes: map[string]schema.Attribute{
			"key": schema.StringAttribute{
				Required:    true,
				Description: "The full key, starting with its mount, for example `secret/db-password` or `password/20/app-admin`.",
				Validators:  []validator.String{validators.CypherKey()},
			},
			"value": schema.StringAttribute{
				Computed: true, Sensitive: true,
				Description: "The decrypted value. For an object stored on the `secret/` mount, the object as JSON (see `values`).",
			},
			"values": schema.MapAttribute{
				Computed: true, Sensitive: true, ElementType: types.StringType,
				Description: "The entries of an object stored on the `secret/` mount; empty for a string value.",
			},
			"lease_timeout": schema.Int64Attribute{Computed: true, Description: "Lease in seconds; the key is removed when it ends."},
			"expire_date":   schema.StringAttribute{Computed: true, Description: "When the lease ends (RFC 3339), or empty."},
		},
	}
}

func (d *cypherDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	if !d.ready(&resp.Diagnostics) {
		return
	}
	var m cypherModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	key := m.Key.ValueString()
	exists, rec, err := d.client.CypherKeyExists(ctx, key)
	if err != nil {
		apiError(&resp.Diagnostics, "Cypher keys", err)
		return
	}
	if !exists {
		resp.Diagnostics.AddAttributeError(path.Root("key"), "No such Cypher key",
			fmt.Sprintf("There is no Cypher key %q. Keys start with their mount, for example secret/%s; mtncloud_cyphers lists them.",
				key, key[strings.Index(key, "/")+1:]))
		return
	}
	secret, err := d.client.ReadCypher(ctx, key)
	if err != nil {
		apiError(&resp.Diagnostics, "the Cypher key", err)
		return
	}
	values, diags := types.MapValueFrom(ctx, types.StringType, secret.Values)
	resp.Diagnostics.Append(diags...)
	if secret.Values == nil {
		values = types.MapValueMust(types.StringType, nil)
	}
	m.Value = types.StringValue(secret.Value)
	m.Values = values
	m.LeaseTimeout = types.Int64Value(asNumber(rec["leaseTimeout"]))
	m.ExpireDate = types.StringValue(asText(rec["expireDate"]))
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}
