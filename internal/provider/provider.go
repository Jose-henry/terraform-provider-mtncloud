// Package provider is the Plugin Framework half of the provider. It is served next to the legacy
// SDKv2 provider through terraform-plugin-mux (D-005); see Server.
package provider

import (
	"context"
	"os"
	"slices"
	"strconv"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/provider/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"

	"github.com/mtn/terraform-provider-mtncloud/internal/client"
	"github.com/mtn/terraform-provider-mtncloud/internal/services/automation"
	"github.com/mtn/terraform-provider-mtncloud/internal/services/catalog"
	"github.com/mtn/terraform-provider-mtncloud/internal/services/cluster"
	"github.com/mtn/terraform-provider-mtncloud/internal/services/compute"
	"github.com/mtn/terraform-provider-mtncloud/internal/services/loadbalancer"
	"github.com/mtn/terraform-provider-mtncloud/internal/services/lookup"
	"github.com/mtn/terraform-provider-mtncloud/internal/services/network"
	"github.com/mtn/terraform-provider-mtncloud/internal/services/secrets"
	"github.com/mtn/terraform-provider-mtncloud/internal/services/storage"
)

var _ provider.Provider = (*mtncloudProvider)(nil)

type mtncloudProvider struct {
	version string
}

// New returns the Framework provider.
func New(version string) func() provider.Provider {
	return func() provider.Provider { return &mtncloudProvider{version: version} }
}

type providerModel struct {
	URL             types.String `tfsdk:"url"`
	AccessToken     types.String `tfsdk:"access_token"`
	TenantSubdomain types.String `tfsdk:"tenant_subdomain"`
	Username        types.String `tfsdk:"username"`
	Password        types.String `tfsdk:"password"`
	Secure          types.Bool   `tfsdk:"secure"`
	Group           types.String `tfsdk:"group"`
	Cloud           types.String `tfsdk:"cloud"`
	Project         types.String `tfsdk:"project"`
}

func (p *mtncloudProvider) Metadata(_ context.Context, _ provider.MetadataRequest, resp *provider.MetadataResponse) {
	resp.TypeName = "mtncloud"
	resp.Version = p.version
}

// Schema must match internal/legacy's provider schema exactly (names, types, flags and
// descriptions): the mux refuses to start otherwise. TestMux_ProviderSchemasMatch guards this.
func (p *mtncloudProvider) Schema(_ context.Context, _ provider.SchemaRequest, resp *provider.SchemaResponse) {
	resp.Schema = schema.Schema{
		Attributes: map[string]schema.Attribute{
			"url": schema.StringAttribute{
				Optional:    true,
				Description: "The URL of the MTN Cloud console where requests will be directed.",
			},
			"access_token": schema.StringAttribute{
				Optional:    true,
				Sensitive:   true,
				Description: "Access Token of MTN Cloud user. This can be used instead of authenticating with Username and Password.",
			},
			"tenant_subdomain": schema.StringAttribute{
				Optional:    true,
				Description: "Your tenant, for a username login: its subdomain or its numeric account ID (both work).",
			},
			"username": schema.StringAttribute{
				Optional:    true,
				Description: "Username of MTN Cloud user for authentication",
			},
			"password": schema.StringAttribute{
				Optional:    true,
				Sensitive:   true,
				Description: "Password of MTN Cloud user for authentication",
			},
			"group": schema.StringAttribute{
				Optional:    true,
				Description: "Default group, by name, for resources that don't set one, and the group to use when your account has several. Can also be set with the MTNCLOUD_GROUP environment variable.",
			},
			"cloud": schema.StringAttribute{
				Optional:    true,
				Description: "The cloud to use when your group has several, by name, for example MTNNG_CLOUD_AZ_1. Can also be set with the MTNCLOUD_CLOUD environment variable.",
			},
			"project": schema.StringAttribute{
				Optional:    true,
				Description: "Default project, by name (for example web-prod), for resources that don't set one. Can also be set with the MTNCLOUD_PROJECT environment variable.",
			},
			"secure": schema.BoolAttribute{
				Optional:    true,
				Description: `Verify the MTN Cloud console's TLS certificate. Defaults to "true". Set to "false" only for a console with a self-signed certificate.`,
			},
		},
	}
}

func (p *mtncloudProvider) Configure(ctx context.Context, req provider.ConfigureRequest, resp *provider.ConfigureResponse) {
	var cfg providerModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	unknown := cfg.Secure.IsUnknown() // an unknown "secure" must never fall back to false
	for _, v := range []types.String{cfg.URL, cfg.AccessToken, cfg.TenantSubdomain, cfg.Username, cfg.Password, cfg.Group, cfg.Cloud, cfg.Project} {
		unknown = unknown || v.IsUnknown()
	}
	if unknown {
		// Values from resources not created yet. The provider stays unconfigured; data sources
		// report this as an error instead of calling the API (see lookup.base.ready).
		resp.Diagnostics.AddWarning("Provider configuration not known yet",
			"The MTN Cloud provider is configured from values that are only known after apply. Its data sources can't be read until those values are known.")
		return
	}

	url := stringOrEnv(cfg.URL, "MTNCLOUD_API_URL")
	if url == "" {
		resp.Diagnostics.AddError("MTN Cloud console URL is not set", "Set url in the provider block, or the MTNCLOUD_API_URL environment variable.")
		return
	}
	secure := true // verify certificates unless the user opts out (B-005)
	if !cfg.Secure.IsNull() {
		secure = cfg.Secure.ValueBool()
	} else if v, err := strconv.ParseBool(os.Getenv("MTNCLOUD_API_SECURE")); err == nil {
		secure = v
	}

	if !secure {
		resp.Diagnostics.AddWarning("TLS certificate verification is off",
			"secure = false (or MTNCLOUD_API_SECURE=false): the provider is not verifying the console's TLS certificate, so a network attacker could intercept credentials. "+
				"Only use this for a console with a self-signed certificate.")
	}

	auth := resolveAuth(cfg)
	c, err := client.New(client.Config{
		URL:      url,
		Token:    auth.token,
		Username: auth.username,
		Password: auth.password,
		Tenant:   auth.tenant,
		Defaults: client.Defaults{
			Group:   stringOrEnv(cfg.Group, "MTNCLOUD_GROUP"),
			Cloud:   stringOrEnv(cfg.Cloud, "MTNCLOUD_CLOUD"),
			Project: stringOrEnv(cfg.Project, "MTNCLOUD_PROJECT"),
		},
		Insecure:  !secure,
		UserAgent: "terraform-provider-mtncloud/" + p.version,
		Logger: func(ctx context.Context, msg string, fields map[string]any) {
			tflog.Debug(ctx, msg, fields)
		},
	})
	if err != nil {
		resp.Diagnostics.AddError("Invalid MTN Cloud provider configuration", err.Error())
		return
	}
	resp.DataSourceData = c
	resp.ResourceData = c
}

type credentials struct{ token, username, password, tenant string }

// resolveAuth picks credentials exactly as the legacy half does (internal/legacy/config.go), so
// both halves of the mux always act as the same user: a username, from the block or
// MTNCLOUD_API_USERNAME, means password login; otherwise the access token is used.
// A stale MTNCLOUD_API_TOKEN therefore can't override a username and password.
func resolveAuth(cfg providerModel) credentials {
	if u := stringOrEnv(cfg.Username, "MTNCLOUD_API_USERNAME"); u != "" {
		return credentials{
			username: u,
			password: stringOrEnv(cfg.Password, "MTNCLOUD_API_PASSWORD"),
			tenant:   stringOrEnv(cfg.TenantSubdomain, "MTNCLOUD_API_TENANT"),
		}
	}
	return credentials{token: stringOrEnv(cfg.AccessToken, "MTNCLOUD_API_TOKEN")}
}

func stringOrEnv(v types.String, env string) string {
	if !v.IsNull() && v.ValueString() != "" {
		return v.ValueString()
	}
	return os.Getenv(env)
}

func (p *mtncloudProvider) Resources(context.Context) []func() resource.Resource {
	return slices.Concat(network.Resources(), compute.Resources(), catalog.Resources(), automation.Resources(), secrets.Resources(), cluster.Resources(), loadbalancer.Resources(), storage.Resources())
}

func (p *mtncloudProvider) DataSources(context.Context) []func() datasource.DataSource {
	return slices.Concat(lookup.DataSources(), network.DataSources(), loadbalancer.DataSources(), storage.DataSources())
}
