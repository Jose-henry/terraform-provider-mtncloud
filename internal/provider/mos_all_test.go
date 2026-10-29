package provider

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/knownvalue"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/statecheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"
)

func TestMOSEverything(t *testing.T) {
	f := newFakeAPI(t)
	m := newMOSFake(t, f)
	src := filepath.Join(t.TempDir(), "page.html")
	if err := os.WriteFile(src, []byte("<h1>one</h1>"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := m.config(f) + `
resource "mtncloud_mos_bucket" "site" {
  name          = "tf-acc-mos-site"
  force_destroy = true
}
resource "mtncloud_mos_bucket" "logs" {
  name          = "tf-acc-mos-logs"
  force_destroy = true
}
resource "mtncloud_mos_bucket_acl" "site" {
  bucket   = mtncloud_mos_bucket.site.name
  acl      = "public-read"
  timeouts = { create = "2m" }
}
resource "mtncloud_mos_bucket_cors" "site" {
  bucket = mtncloud_mos_bucket.site.name
  rule = [
    {
      allowed_origins = ["https://app.example.com", "https://www.example.com"],
      allowed_methods = ["GET", "HEAD"],
      max_age_seconds = 600
    },
    {
      id              = "uploads",
      allowed_origins = ["*"],
      allowed_methods = ["PUT"],
      allowed_headers = ["*"]
    },
  ]
}
resource "mtncloud_mos_bucket_lifecycle" "site" {
  bucket = mtncloud_mos_bucket.site.name
  rule = [
    {
      id              = "tmp",
      prefix          = "tmp/",
      expiration_days = 7
    },
    {
      id                           = "uploads",
      abort_incomplete_upload_days = 2
    },
  ]
}
resource "mtncloud_mos_bucket_logging" "site" {
  bucket        = mtncloud_mos_bucket.site.name
  target_bucket = mtncloud_mos_bucket.logs.name
  target_prefix = "site/"
}
resource "mtncloud_mos_object" "hello" {
  bucket   = mtncloud_mos_bucket.site.name
  key      = "hello.txt"
  content  = "hello"
  metadata = { team = "web" }
}
resource "mtncloud_mos_object" "page" {
  bucket   = mtncloud_mos_bucket.site.name
  key      = "site/index.html"
  source   = "` + filepath.ToSlash(src) + `"
  timeouts = { update = "2m" }
}
resource "mtncloud_mos_object_acl" "page" {
  bucket   = mtncloud_mos_object.page.bucket
  key      = mtncloud_mos_object.page.key
  acl      = "public-read"
  timeouts = { create = "2m" }
}

data "mtncloud_mos_credentials" "k" {}
data "mtncloud_mos_buckets" "all" {
  depends_on = [mtncloud_mos_bucket.site, mtncloud_mos_bucket.logs]
}
data "mtncloud_mos_bucket" "site" {
  name       = mtncloud_mos_bucket.site.name
  depends_on = [mtncloud_mos_bucket_acl.site, mtncloud_mos_bucket_cors.site, mtncloud_mos_bucket_lifecycle.site, mtncloud_mos_bucket_logging.site]
}
data "mtncloud_mos_bucket_cors" "site" {
  bucket = mtncloud_mos_bucket_cors.site.bucket
}
data "mtncloud_mos_bucket_lifecycle" "site" {
  bucket = mtncloud_mos_bucket_lifecycle.site.bucket
}
data "mtncloud_mos_bucket_logging" "site" {
  bucket = mtncloud_mos_bucket_logging.site.bucket
}
data "mtncloud_mos_bucket_acl" "site" {
  bucket = mtncloud_mos_bucket_acl.site.bucket
}
data "mtncloud_mos_objects" "site" {
  bucket     = mtncloud_mos_bucket.site.name
  delimiter  = "/"
  depends_on = [mtncloud_mos_object.hello, mtncloud_mos_object.page]
}
data "mtncloud_mos_object" "hello" {
  bucket          = mtncloud_mos_object.hello.bucket
  key             = mtncloud_mos_object.hello.key
  include_content = true
}
data "mtncloud_mos_object_acl" "page" {
  bucket = mtncloud_mos_object_acl.page.bucket
  key    = mtncloud_mos_object_acl.page.key
}
`
	str := knownvalue.StringExact
	check := statecheck.ExpectKnownValue
	empty := resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()}}
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories(),
		CheckDestroy: func(s *terraform.State) error {
			for _, b := range []string{"tf-acc-mos-site", "tf-acc-mos-logs"} {
				if err := m.checkDestroyed(b)(s); err != nil {
					return err
				}
			}
			return nil
		},
		Steps: []resource.TestStep{
			{
				Config: cfg,
				ConfigStateChecks: []statecheck.StateCheck{
					check("mtncloud_mos_bucket_acl.site", tfjsonpath.New("grants"), knownvalue.ListExact([]knownvalue.Check{str("AllUsers:READ"), str("owner:FULL_CONTROL")})),
					check("mtncloud_mos_object.hello", tfjsonpath.New("etag"), str("5d41402abc4b2a76b9719d911017c592")),
					check("mtncloud_mos_object.hello", tfjsonpath.New("size"), knownvalue.Int64Exact(5)),
					check("data.mtncloud_mos_credentials.k", tfjsonpath.New("source"), str("environment")),
					check("data.mtncloud_mos_buckets.all", tfjsonpath.New("buckets"), knownvalue.ListSizeExact(2)),
					check("data.mtncloud_mos_buckets.all", tfjsonpath.New("buckets").AtSliceIndex(1).AtMapKey("console_name"), str("tf-acc-mos-site")),
					check("data.mtncloud_mos_bucket.site", tfjsonpath.New("acl"), str("public-read")),
					check("data.mtncloud_mos_bucket.site", tfjsonpath.New("cors_rule"), knownvalue.SetSizeExact(2)),
					check("data.mtncloud_mos_bucket.site", tfjsonpath.New("lifecycle_rule"), knownvalue.SetSizeExact(2)),
					check("data.mtncloud_mos_bucket.site", tfjsonpath.New("logging_target_bucket"), str("tf-acc-mos-logs")),
					check("data.mtncloud_mos_bucket.site", tfjsonpath.New("object_lock_enabled"), knownvalue.Bool(false)),
					check("data.mtncloud_mos_bucket.site", tfjsonpath.New("console_id"), knownvalue.NotNull()),
					check("data.mtncloud_mos_bucket_cors.site", tfjsonpath.New("rule"), knownvalue.SetSizeExact(2)),
					check("data.mtncloud_mos_bucket_lifecycle.site", tfjsonpath.New("rule"), knownvalue.SetSizeExact(2)),
					check("data.mtncloud_mos_bucket_logging.site", tfjsonpath.New("target_prefix"), str("site/")),
					check("data.mtncloud_mos_bucket_acl.site", tfjsonpath.New("acl"), str("public-read")),
					check("data.mtncloud_mos_objects.site", tfjsonpath.New("objects"), knownvalue.ListSizeExact(2)),
					check("data.mtncloud_mos_object.hello", tfjsonpath.New("content"), str("hello")),
					check("data.mtncloud_mos_object.hello", tfjsonpath.New("metadata").AtMapKey("team"), str("web")),
					check("data.mtncloud_mos_object_acl.page", tfjsonpath.New("acl"), str("public-read")),
				},
			},
			{Config: cfg, ConfigPlanChecks: empty},
			{
				// Editing the source file uploads it again, in place.
				PreConfig: func() {
					if err := os.WriteFile(src, []byte("<h1>two</h1>"), 0o600); err != nil {
						t.Fatal(err)
					}
				},
				Config: cfg,
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction("mtncloud_mos_object.page", plancheck.ResourceActionUpdate)}},
			},
			// The upload kept the page's ACL: nothing left to do.
			{Config: cfg, ConfigPlanChecks: empty},
			{Config: cfg, ResourceName: "mtncloud_mos_bucket_cors.site", ImportState: true, ImportStateVerify: true},
			{Config: cfg, ResourceName: "mtncloud_mos_bucket_lifecycle.site", ImportState: true, ImportStateVerify: true},
			{Config: cfg, ResourceName: "mtncloud_mos_bucket_logging.site", ImportState: true, ImportStateVerify: true},
			{Config: cfg, ResourceName: "mtncloud_mos_bucket_acl.site", ImportState: true, ImportStateVerify: true, ImportStateVerifyIgnore: []string{"timeouts"}},
			{Config: cfg, ResourceName: "mtncloud_mos_object_acl.page", ImportState: true, ImportStateVerify: true, ImportStateVerifyIgnore: []string{"timeouts"}},
			{
				Config: cfg, ResourceName: "mtncloud_mos_object.hello", ImportState: true, ImportStateVerify: true,
				ImportStateVerifyIgnore: []string{"content", "bypass_governance_on_destroy"},
			},
		},
	})
}

// An object written outside Terraform keeps its ACL when Terraform takes it over by uploading.
func TestMOSObject_CreateKeepsACL(t *testing.T) {
	f := newFakeAPI(t)
	m := newMOSFake(t, f)
	m.buckets["tf-acc-mos-keep"] = map[string]string{}
	m.addObject("tf-acc-mos-keep", "page.html")
	m.acls["tf-acc-mos-keep/page.html"] = "public-read"
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: factories(),
		Steps: []resource.TestStep{{
			Config: m.config(f) + `
resource "mtncloud_mos_object" "page" {
  bucket  = "tf-acc-mos-keep"
  key     = "page.html"
  content = "<h1>new</h1>"
}
`,
			Check: func(*terraform.State) error {
				m.mu.Lock()
				defer m.mu.Unlock()
				if got := m.acls["tf-acc-mos-keep/page.html"]; got != "public-read" {
					return fmt.Errorf("ACL %q after the upload, want public-read", got)
				}
				return nil
			},
		}},
	})
}

func TestMOSSettings_PlanTimeChecks(t *testing.T) {
	f := newFakeAPI(t)
	m := newMOSFake(t, f)
	for name, c := range map[string]struct{ config, want string }{
		"lifecycle rule with no action": {`
resource "mtncloud_mos_bucket_lifecycle" "x" {
  bucket = "tf-acc-x"
  rule = [
    {
      id = "noop"
    },
  ]
}`, `Rule "noop" sets no action`},
		"days and date": {`
resource "mtncloud_mos_bucket_lifecycle" "x" {
  bucket = "tf-acc-x"
  rule = [
    {
      id              = "both",
      expiration_days = 1,
      expiration_date = "2027-01-01"
    },
  ]
}`, `sets expiration_days and expiration_date`},
		"bad date": {`
resource "mtncloud_mos_bucket_lifecycle" "x" {
  bucket = "tf-acc-x"
  rule = [
    {
      id              = "d",
      expiration_date = "01/02/2027"
    },
  ]
}`, `isn't a date as YYYY-MM-DD`},
		"lower-case CORS method": {`
resource "mtncloud_mos_bucket_cors" "x" {
  bucket = "tf-acc-x"
  rule = [
    {
      allowed_origins = ["*"],
      allowed_methods = ["get"]
    },
  ]
}`, `value must be one of`},
		"retention without a date": {`
resource "mtncloud_mos_object" "x" {
  bucket         = "tf-acc-x"
  key            = "a"
  content        = "a"
  retention_mode = "GOVERNANCE"
}`, `retain_until`},
		"two contents": {`
resource "mtncloud_mos_object" "x" {
  bucket         = "tf-acc-x"
  key            = "a"
  content        = "a"
  content_base64 = "YQ=="
}`, `Invalid Attribute Combination`},
		"metadata name with capitals": {`
resource "mtncloud_mos_object" "x" {
  bucket   = "tf-acc-x"
  key      = "a"
  content  = "a"
  metadata = { Team = "web" }
}`, `(?s)Metadata name has capitals.*Use "team"`},
		"bad timeout": {`
resource "mtncloud_mos_bucket_acl" "x" {
  bucket   = "tf-acc-x"
  acl      = "private"
  timeouts = { create = "soon" }
}`, `timeouts`},
		"lock days and years": {`
resource "mtncloud_mos_bucket_object_lock" "x" {
  bucket = "tf-acc-x"
  days   = 1
  years  = 1
}`, `Invalid Attribute Combination`},
	} {
		t.Run(name, func(t *testing.T) {
			resource.UnitTest(t, resource.TestCase{
				ProtoV6ProviderFactories: factories(),
				Steps:                    []resource.TestStep{{Config: m.config(f) + c.config, PlanOnly: true, ExpectError: regexp.MustCompile(c.want)}},
			})
		})
	}
}
