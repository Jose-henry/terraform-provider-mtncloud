// Package storage holds MOS (MTN Object Storage): buckets, their settings and objects, and the data
// sources that read them. A bucket is created in MOS and registered in the console (Infrastructure
// > Storage > Buckets), the way MTN's own flow does it; everything else talks to MOS with the
// tenant's keys from Cypher (internal/client/mos*.go explains why, and the order things happen in).
package storage

import (
	"context"
	"fmt"
	"net"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"

	"github.com/mtn/terraform-provider-mtncloud/internal/client"
)

// Resources lists every resource in this package, for the provider to register.
func Resources() []func() resource.Resource {
	return []func() resource.Resource{
		newBucket, newBucketACL, newBucketCORS, newBucketLifecycle, newBucketLogging, newBucketObjectLock,
		newObject, newObjectACL, newArchive, newArchiveFile, newArchiveLink,
	}
}

// DataSources lists every data source in this package, for the provider to register.
func DataSources() []func() datasource.DataSource {
	return []func() datasource.DataSource{
		newCredentials, newBuckets, newBucketData,
		newBucketACLData, newBucketCORSData, newBucketLifecycleData, newBucketLoggingData, newBucketObjectLockData,
		newObjects, newObjectData, newObjectACLData, newArchiveData, newArchivesData, newArchiveFilesData,
	}
}

// mosTimeout bounds a MOS write and the wait for every node to see it, unless timeouts {} says
// otherwise. Writes usually settle in under ten seconds.
const mosTimeout = 5 * time.Minute

// base gives every resource and data source here its client.
type base struct{ client *client.Client }

func (b *base) configure(data any, diags *diag.Diagnostics) {
	if data == nil {
		return
	}
	c, ok := data.(*client.Client)
	if !ok {
		diags.AddError("Unexpected provider data", fmt.Sprintf("expected *client.Client, got %T. This is a provider bug.", data))
		return
	}
	b.client = c
}

func (b *base) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	b.configure(req.ProviderData, &resp.Diagnostics)
}

func (b *base) ready(diags *diag.Diagnostics) bool {
	if b.client == nil {
		diags.AddError("MTN Cloud provider is not configured yet",
			"The provider block uses values that are only known after apply. Use values known at plan time (variables, "+
				"environment variables), or apply the resources they come from first.")
		return false
	}
	return true
}

// dataBase is base for data sources.
type dataBase struct{ base }

func (b *dataBase) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	b.configure(req.ProviderData, &resp.Diagnostics)
}

// --- bucket names

var bucketNameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]$`)

// bucketNameProblem explains why a bucket name isn't allowed, or returns "".
func bucketNameProblem(name string) string {
	switch {
	case !bucketNameRE.MatchString(name):
		return "use 3 to 63 lower-case letters, digits, dots and hyphens, starting and ending with a letter or digit"
	case strings.Contains(name, ".."), strings.Contains(name, ".-"), strings.Contains(name, "-."):
		return "dots can't be next to each other or to a hyphen"
	case net.ParseIP(name) != nil:
		return "it can't look like an IP address"
	}
	return ""
}

type bucketNameValidator struct{}

func (bucketNameValidator) Description(context.Context) string {
	return "a bucket name: 3 to 63 lower-case letters, digits, dots and hyphens"
}
func (v bucketNameValidator) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}
func (bucketNameValidator) ValidateString(_ context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	if p := bucketNameProblem(req.ConfigValue.ValueString()); p != "" {
		resp.Diagnostics.AddAttributeError(req.Path, "Invalid bucket name", fmt.Sprintf("%q isn't a valid bucket name: %s.", req.ConfigValue.ValueString(), p))
	}
}

func bucketNameValidators() []validator.String { return []validator.String{bucketNameValidator{}} }

// bucketGone reports, after a read error, that the bucket no longer exists in MOS: the setting goes
// with it, so its resource leaves state (rule 8).
func bucketGone(err error) bool { return client.IsMOSCode(err, "NoSuchBucket") }

var (
	rootBucket = path.Root("bucket")
	rootKey    = path.Root("key")
	rootName   = path.Root("name")
)

func containsAny(s string, subs ...string) bool {
	for _, x := range subs {
		if strings.Contains(s, x) {
			return true
		}
	}
	return false
}

func sortedBuckets(in []client.MOSBucket) []client.MOSBucket {
	out := append([]client.MOSBucket{}, in...)
	slices.SortFunc(out, func(a, b client.MOSBucket) int { return strings.Compare(a.Name, b.Name) })
	return out
}
