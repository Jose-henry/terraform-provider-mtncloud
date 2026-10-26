package main

import (
	"context"
	"flag"
	"log"

	"github.com/hashicorp/terraform-plugin-go/tfprotov6/tf6server"

	"github.com/mtn/terraform-provider-mtncloud/internal/provider"
)

// version is set by goreleaser at release time.
var version = "dev"

func main() {
	debug := flag.Bool("debug", false, "start the provider in debug mode (for delve)")
	flag.Parse()

	ctx := context.Background()
	server, err := provider.Server(ctx, version)
	if err != nil {
		log.Fatal(err)
	}
	var opts []tf6server.ServeOpt
	if *debug {
		opts = append(opts, tf6server.WithManagedDebug())
	}
	if err := tf6server.Serve("registry.terraform.io/mtn/mtncloud", server, opts...); err != nil {
		log.Fatal(err)
	}
}
