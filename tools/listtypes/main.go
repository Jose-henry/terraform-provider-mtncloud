// Command listtypes prints every resource and data source the provider serves, one per line, as
// "resource <name> <access>" or "data <name> <access>". <access> is "master" when the description
// starts with masterOnlyPrefix (D-004), otherwise "-". It asks the muxed server (legacy and
// Framework together), exactly as Terraform does. scripts/gen-features.py reads it to build the
// features guide, so the guide always matches what the provider really serves and says.
package main

import (
	"context"
	"fmt"
	"log"
	"slices"
	"strings"

	"github.com/hashicorp/terraform-plugin-go/tfprotov6"

	"github.com/mtn/terraform-provider-mtncloud/internal/provider"
)

// masterOnlyPrefix opens the description of every master-only type (rule 23).
const masterOnlyPrefix = "Master tenant only"

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
	emit := func(kind string, schemas map[string]*tfprotov6.Schema) {
		names := make([]string, 0, len(schemas))
		for n := range schemas {
			names = append(names, n)
		}
		slices.Sort(names)
		for _, n := range names {
			access := "-"
			if s := schemas[n]; s != nil && s.Block != nil && strings.HasPrefix(s.Block.Description, masterOnlyPrefix) {
				access = "master"
			}
			fmt.Println(kind, n, access)
		}
	}
	emit("resource", resp.ResourceSchemas)
	emit("data", resp.DataSourceSchemas)
}
