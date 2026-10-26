// Command listtypes prints every registered resource and data source, one per line, as
// "resource <name>" or "data <name>". scripts/gen-features.py reads it to build the features guide,
// so the guide always matches what the provider really serves.
package main

import (
	"fmt"
	"sort"

	"github.com/mtn/terraform-provider-mtncloud/internal/legacy"
)

func main() {
	p := legacy.Provider()
	emit := func(kind string, names []string) {
		sort.Strings(names)
		for _, n := range names {
			fmt.Println(kind, n)
		}
	}
	resources := make([]string, 0, len(p.ResourcesMap))
	for n := range p.ResourcesMap {
		resources = append(resources, n)
	}
	dataSources := make([]string, 0, len(p.DataSourcesMap))
	for n := range p.DataSourcesMap {
		dataSources = append(dataSources, n)
	}
	emit("resource", resources)
	emit("data", dataSources)
}
