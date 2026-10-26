// Command listtypes prints every resource and data source the provider serves, one per line, as
// "resource <name>" or "data <name>". It asks the muxed server (legacy and Framework together),
// exactly as Terraform does. scripts/gen-features.py reads it to build the features guide, so the
// guide always matches what the provider really serves.
package main

import (
	"context"
	"fmt"
	"log"
	"slices"

	"github.com/hashicorp/terraform-plugin-go/tfprotov6"

	"github.com/mtn/terraform-provider-mtncloud/internal/provider"
)

func main() {
	ctx := context.Background()
	server, err := provider.Server(ctx, "dev")
	if err != nil {
		log.Fatal(err)
	}
	resp, err := server().GetProviderSchema(ctx, &tfprotov6.GetProviderSchemaRequest{})
	if err != nil {
		log.Fatal(err)
	}
	for _, d := range resp.Diagnostics {
		if d.Severity == tfprotov6.DiagnosticSeverityError {
			log.Fatalf("%s: %s", d.Summary, d.Detail)
		}
	}
	emit := func(kind string, names []string) {
		slices.Sort(names)
		for _, n := range names {
			fmt.Println(kind, n)
		}
	}
	var resources, dataSources []string
	for n := range resp.ResourceSchemas {
		resources = append(resources, n)
	}
	for n := range resp.DataSourceSchemas {
		dataSources = append(dataSources, n)
	}
	emit("resource", resources)
	emit("data", dataSources)
}
