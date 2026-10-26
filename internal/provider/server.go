package provider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-mux/tf5to6server"
	"github.com/hashicorp/terraform-plugin-mux/tf6muxserver"

	"github.com/mtn/terraform-provider-mtncloud/internal/legacy"
)

// Server returns the muxed provider server: the legacy SDKv2 provider (upgraded to protocol 6)
// and the Framework provider, served as one. main.go and the provider tests both use it.
func Server(ctx context.Context, version string) (func() tfprotov6.ProviderServer, error) {
	upgraded, err := tf5to6server.UpgradeServer(ctx, legacy.Provider().GRPCProvider)
	if err != nil {
		return nil, err
	}
	mux, err := tf6muxserver.NewMuxServer(ctx,
		func() tfprotov6.ProviderServer { return upgraded },
		providerserver.NewProtocol6(New(version)()),
	)
	if err != nil {
		return nil, err
	}
	return mux.ProviderServer, nil
}
