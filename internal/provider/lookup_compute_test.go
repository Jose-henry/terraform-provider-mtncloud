package provider

import (
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/knownvalue"
	"github.com/hashicorp/terraform-plugin-testing/statecheck"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"
)

func TestRouters_DataSource(t *testing.T) {
	f, _, _ := newRouterFakes(t)
	cfg := routerConfig(f, `  name = "edge"
  networks = [mtncloud_network.app.id]
  external_network = "public-network-02"`)
	list := func(name, regex string) string {
		return `
data "mtncloud_routers" "` + name + `" {
  name_regex = "` + regex + `"
}
`
	}
	all := tfjsonpath.New("routers")
	at := func(attr string) tfjsonpath.Path { return all.AtSliceIndex(0).AtMapKey(attr) }
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories(),
		Steps: []resource.TestStep{
			{Config: cfg},
			{
				// A router that exists, found by name; and a filter that matches none.
				Config: cfg + list("edge", "^ed") + list("none", "^nope"),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue("data.mtncloud_routers.edge", all, knownvalue.ListSizeExact(1)),
					statecheck.ExpectKnownValue("data.mtncloud_routers.edge", at("name"), knownvalue.StringExact("edge")),
					statecheck.ExpectKnownValue("data.mtncloud_routers.edge", at("external_network"), knownvalue.StringExact("public-network-02")),
					statecheck.ExpectKnownValue("data.mtncloud_routers.edge", at("networks"), knownvalue.ListSizeExact(1)),
					statecheck.ExpectKnownValue("data.mtncloud_routers.none", all, knownvalue.ListSizeExact(0)),
				},
			},
		},
	})
}

func TestInstances_DataSource(t *testing.T) {
	f, _ := newInstFakes(t)
	cfg := instConfig(f, `  name = "app-1"
  plan = "G1S1"
  networks = ["9301"]
`)
	list := func(name, filters string) string {
		return `
data "mtncloud_instances" "` + name + `" {
` + filters + `
}
`
	}
	at := func(i int, attr string) tfjsonpath.Path {
		return tfjsonpath.New("instances").AtSliceIndex(i).AtMapKey(attr)
	}
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories(),
		Steps: []resource.TestStep{
			{Config: cfg},
			{
				Config: cfg + list("web", `  project = "web-prod"`) + list("other", `  project = "data"`) + list("named", `  name_regex = "^app-"`),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue("data.mtncloud_instances.web", tfjsonpath.New("instances"), knownvalue.ListSizeExact(1)),
					statecheck.ExpectKnownValue("data.mtncloud_instances.web", at(0, "name"), knownvalue.StringExact("app-1")),
					statecheck.ExpectKnownValue("data.mtncloud_instances.web", at(0, "project"), knownvalue.StringExact("web-prod")),
					statecheck.ExpectKnownValue("data.mtncloud_instances.web", at(0, "status"), knownvalue.StringExact("running")),
					statecheck.ExpectKnownValue("data.mtncloud_instances.other", tfjsonpath.New("instances"), knownvalue.ListSizeExact(0)),
					statecheck.ExpectKnownValue("data.mtncloud_instances.named", tfjsonpath.New("instances"), knownvalue.ListSizeExact(1)),
				},
			},
			{
				Config:      cfg + list("bad", `  project = "nope"`),
				ExpectError: regexp.MustCompile(`(?s)Unknown project.*no project named "nope"`),
			},
		},
	})
}

func TestInstance_DataSource(t *testing.T) {
	f, s := newInstFakes(t)
	cfg := instConfig(f, `  name = "app-1"
  plan = "G1S1"
  networks = ["9301"]
`)
	one := func(attrs string) string {
		return cfg + `
data "mtncloud_instance" "one" {
` + attrs + `
}
`
	}
	check := func(attr string, v knownvalue.Check) statecheck.StateCheck {
		return statecheck.ExpectKnownValue("data.mtncloud_instance.one", tfjsonpath.New(attr), v)
	}
	byName := []statecheck.StateCheck{
		check("name", knownvalue.StringExact("app-1")),
		check("project", knownvalue.StringExact("web-prod")),
		check("status", knownvalue.StringExact("running")),
		check("id", knownvalue.NotNull()),
	}
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories(),
		Steps: []resource.TestStep{
			{Config: cfg},
			{Config: one(`  name = "app-1"`), ConfigStateChecks: byName},
			{Config: one(`  id = mtncloud_instance.app.id`), ConfigStateChecks: byName},
			{Config: one(`  name = "app-1"
  project = "web-prod"`), ConfigStateChecks: byName},
			{Config: one(`  name = "nope"`), ExpectError: regexp.MustCompile(`No such instance`)},
			{Config: one(`  id = 999999`), ExpectError: regexp.MustCompile(`No such instance`)},
			{Config: one(`  name = "app-1"
  project = "data"`), ExpectError: regexp.MustCompile(`(?s)No such instance.*in\s+project\s+data`)},
			{Config: one(`  id = mtncloud_instance.app.id
  project = "data"`), ExpectError: regexp.MustCompile(`Instance in another project`)},
			{Config: one(``), ExpectError: regexp.MustCompile(`Exactly one of these attributes must be configured`)},
			{
				// Same name twice: refused, naming both IDs.
				PreConfig: func() {
					s.mu.Lock()
					defer s.mu.Unlock()
					for id, in := range s.insts {
						cp := map[string]any{}
						for k, v := range in {
							cp[k] = v
						}
						cp["id"] = id + 10000
						s.insts[id+10000] = cp
						break
					}
				},
				Config:      one(`  name = "app-1"`),
				ExpectError: regexp.MustCompile(`(?s)Several instances match.*Add\s+project`),
			},
			{
				PreConfig: func() {
					s.mu.Lock()
					defer s.mu.Unlock()
					for id := range s.insts {
						if id > 10000 { // the copy made above
							delete(s.insts, id)
						}
					}
				},
				Config: cfg,
			},
		},
	})
}

func TestNetwork_DataSource(t *testing.T) {
	f, _, _ := newRouterFakes(t)
	cfg := routerConfig(f, `  name = "edge"
  networks = [mtncloud_network.app.id]`)
	one := func(attrs string) string {
		return cfg + `
data "mtncloud_network" "one" {
` + attrs + `
}
`
	}
	check := func(attr string, v knownvalue.Check) statecheck.StateCheck {
		return statecheck.ExpectKnownValue("data.mtncloud_network.one", tfjsonpath.New(attr), v)
	}
	found := []statecheck.StateCheck{
		check("name", knownvalue.StringExact("app-net")),
		check("project", knownvalue.StringExact("web-prod")),
		check("cloud", knownvalue.StringExact("MTNNG_CLOUD_AZ_1")),
		check("cidr", knownvalue.StringExact("10.20.1.0/24")),
	}
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories(),
		Steps: []resource.TestStep{
			{Config: cfg},
			{Config: one(`  name = "app-net"`), ConfigStateChecks: found},
			{Config: one(`  id = mtncloud_network.app.id`), ConfigStateChecks: found},
			{Config: one(`  name = "app-net"
  project = "web-prod-Acme"`), ConfigStateChecks: found},
			{Config: one(`  name = "app-net"
  project = "data"`), ExpectError: regexp.MustCompile(`(?s)No such network.*in\s+project\s+data`)},
			{Config: one(`  name = "nope"`), ExpectError: regexp.MustCompile(`No such network`)},
			{Config: one(`  id = mtncloud_network.app.id
  project = "data"`), ExpectError: regexp.MustCompile(`Network in another project`)},
			{Config: cfg},
		},
	})
}

func TestRouter_DataSource(t *testing.T) {
	f, _, _ := newRouterFakes(t)
	cfg := routerConfig(f, `  name = "edge"
  networks = [mtncloud_network.app.id]
  external_network = "public-network-02"`)
	one := func(attrs string) string {
		return cfg + "\ndata \"mtncloud_router\" \"one\" {\n" + attrs + "\n}\n"
	}
	found := []statecheck.StateCheck{
		statecheck.ExpectKnownValue("data.mtncloud_router.one", tfjsonpath.New("name"), knownvalue.StringExact("edge")),
		statecheck.ExpectKnownValue("data.mtncloud_router.one", tfjsonpath.New("external_network"), knownvalue.StringExact("public-network-02")),
		statecheck.ExpectKnownValue("data.mtncloud_router.one", tfjsonpath.New("networks"), knownvalue.ListSizeExact(1)),
	}
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories(),
		Steps: []resource.TestStep{
			{Config: cfg},
			{Config: one(`  name = "edge"`), ConfigStateChecks: found},
			{Config: one(`  id = mtncloud_router.edge.id`), ConfigStateChecks: found},
			{Config: one(`  name = "nope"`), ExpectError: regexp.MustCompile(`No such router`)},
			{Config: one(`  id = 999999`), ExpectError: regexp.MustCompile(`No such router`)},
			{Config: cfg},
		},
	})
}

func TestProject_DataSource(t *testing.T) {
	f := newFakeAPI(t)
	one := func(attrs string) string {
		return f.providerConfig() + "\ndata \"mtncloud_project\" \"one\" {\n" + attrs + "\n}\n"
	}
	check := func(attr string, v knownvalue.Check) statecheck.StateCheck {
		return statecheck.ExpectKnownValue("data.mtncloud_project.one", tfjsonpath.New(attr), v)
	}
	found := []statecheck.StateCheck{
		check("id", knownvalue.Int64Exact(9201)),
		check("name", knownvalue.StringExact("web-prod")),
		check("full_name", knownvalue.StringExact("web-prod-Acme")),
		check("group", knownvalue.StringExact("MTNNG_CLOUD_AZ_1")),
		check("cloud", knownvalue.StringExact("MTNNG_CLOUD_AZ_1")),
	}
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories(),
		Steps: []resource.TestStep{
			{Config: one(`  name = "web-prod"`), ConfigStateChecks: found},
			{Config: one(`  name = "web-prod-Acme"`), ConfigStateChecks: found},
			{Config: one(`  id = 9201`), ConfigStateChecks: found},
			{Config: one(`  name = "nope"`), ExpectError: regexp.MustCompile(`(?s)No such project.*no project named "nope"`)},
			{Config: one(`  id = 1`), ExpectError: regexp.MustCompile(`No such project`)},
			{Config: one(`  name = "web-prod"`)},
		},
	})
}

func TestSecurityGroup_DataSource(t *testing.T) {
	f := newFakeAPI(t)
	f.sg = newSGStore()
	cfg := sgConfig(f, webRules)
	one := func(attrs string) string {
		return cfg + "\ndata \"mtncloud_security_group\" \"one\" {\n" + attrs + "\n}\n"
	}
	check := func(attr string, v knownvalue.Check) statecheck.StateCheck {
		return statecheck.ExpectKnownValue("data.mtncloud_security_group.one", tfjsonpath.New(attr), v)
	}
	found := []statecheck.StateCheck{
		check("name", knownvalue.StringExact("web")),
		check("project", knownvalue.StringExact("web-prod")),
		check("description", knownvalue.StringExact("web servers")),
		check("rules", knownvalue.ListSizeExact(2)),
	}
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories(),
		Steps: []resource.TestStep{
			{Config: cfg},
			{Config: one(`  name = "web"`), ConfigStateChecks: found},
			{Config: one(`  id = mtncloud_security_group.web.id`), ConfigStateChecks: found},
			{Config: one(`  name = "web"
  project = "web-prod-Acme"`), ConfigStateChecks: found},
			{Config: one(`  name = "web"
  project = "data"`), ExpectError: regexp.MustCompile(`(?s)No such security group.*in\s+project\s+data`)},
			{Config: one(`  id = mtncloud_security_group.web.id
  project = "data"`), ExpectError: regexp.MustCompile(`Security group in another project`)},
			{Config: one(`  id = "abc"`), ExpectError: regexp.MustCompile(`Invalid security group ID`)},
			{Config: one(`  name = "nope"`), ExpectError: regexp.MustCompile(`No such security group`)},
			{Config: cfg},
		},
	})
}

func TestCatalogItem_DataSource(t *testing.T) {
	f := newFakeAPI(t)
	one := func(attrs string) string {
		return f.providerConfig() + "\ndata \"mtncloud_catalog_item\" \"one\" {\n" + attrs + "\n}\n"
	}
	in := func(i int, attr string) tfjsonpath.Path {
		return tfjsonpath.New("inputs").AtSliceIndex(i).AtMapKey(attr)
	}
	check := func(p tfjsonpath.Path, v knownvalue.Check) statecheck.StateCheck {
		return statecheck.ExpectKnownValue("data.mtncloud_catalog_item.one", p, v)
	}
	// The form's own inputs and its field groups', in display order.
	form := []statecheck.StateCheck{
		check(tfjsonpath.New("id"), knownvalue.Int64Exact(22)),
		check(tfjsonpath.New("inputs"), knownvalue.ListSizeExact(4)),
		check(in(0, "name"), knownvalue.StringExact("groupName")),
		check(in(1, "name"), knownvalue.StringExact("cloud")),
		check(in(1, "default"), knownvalue.StringExact("4")),
		check(in(2, "name"), knownvalue.StringExact("instanceName")),
		check(in(2, "help"), knownvalue.StringExact("Lower case and dashes.")),
		check(in(3, "required"), knownvalue.Bool(false)),
	}
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories(),
		Steps: []resource.TestStep{
			{Config: one(`  name = "Create Instance"`), ConfigStateChecks: form},
			{Config: one(`  id = 22`), ConfigStateChecks: form},
			{Config: one(`  name = "MTN Object Storage"`), ConfigStateChecks: []statecheck.StateCheck{
				check(tfjsonpath.New("inputs"), knownvalue.ListSizeExact(0)),
			}},
			{Config: one(`  name = "nope"`), ExpectError: regexp.MustCompile(`(?s)No such catalog item.*You can order:\s+Create\s+Instance`)},
			{Config: one(`  id = 1`), ExpectError: regexp.MustCompile(`No such catalog item`)},
			{Config: one(`  id = 22`)},
		},
	})
}
