package storage

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/setvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/mtn/terraform-provider-mtncloud/internal/client"
)

// --- ACL

type aclModel struct {
	ID     types.String `tfsdk:"id"`
	Bucket types.String `tfsdk:"bucket"`
	ACL    types.String `tfsdk:"acl"`
	Grants types.List   `tfsdk:"grants"`
}

func newBucketACL() resource.Resource { return &setting[aclModel]{spec: aclSpec()} }

func aclSpec() settingSpec[aclModel] {
	return settingSpec[aclModel]{
		typeName: "mos_bucket_acl",
		about: "Who may read and write a MOS bucket, as a canned ACL. Destroy sets it back to `private`. " +
			"A bucket's ACL covers listing and adding objects; each object has its own (`mtncloud_mos_object_acl`).",
		attributes: map[string]schema.Attribute{
			"acl": schema.StringAttribute{
				Description: "`private` (only the tenant), `public-read` (anyone may list the bucket), `public-read-write` " +
					"(anyone may also add and delete objects: rarely wise) or `authenticated-read` (any MOS user may list it).",
				Required:   true,
				Validators: []validator.String{stringvalidator.OneOf(client.CannedACLs()...)},
			},
			"grants": schema.ListAttribute{
				Description: "The grants MOS holds, as `<grantee>:<permission>`, for example `AllUsers:READ`.",
				ElementType: types.StringType,
				Computed:    true,
			},
		},
		keys: func(m *aclModel) (*types.String, *types.String) { return &m.ID, &m.Bucket },
		read: func(ctx context.Context, c *client.Client, bucket string, m *aclModel, diags *diag.Diagnostics) {
			a, err := c.GetBucketACL(ctx, bucket)
			if err != nil {
				readError(diags, "mos_bucket_acl", err)
				return
			}
			m.ACL = types.StringValue(a.Canned) // "custom" when set by other tools: the plan puts it back
			m.Grants, _ = types.ListValueFrom(ctx, types.StringType, a.Grants)
		},
		write: func(ctx context.Context, c *client.Client, bucket string, m *aclModel) error {
			if err := c.PutBucketACL(ctx, bucket, m.ACL.ValueString()); err != nil {
				return err
			}
			a, err := c.GetBucketACL(ctx, bucket)
			if err != nil {
				return err
			}
			m.Grants, _ = types.ListValueFrom(ctx, types.StringType, a.Grants)
			return nil
		},
		clear: func(ctx context.Context, c *client.Client, bucket string) error {
			return c.PutBucketACL(ctx, bucket, "private")
		},
	}
}

// --- CORS

type corsModel struct {
	ID     types.String    `tfsdk:"id"`
	Bucket types.String    `tfsdk:"bucket"`
	Rules  []corsRuleModel `tfsdk:"rule"`
}

type corsRuleModel struct {
	ID             types.String `tfsdk:"id"`
	AllowedOrigins types.Set    `tfsdk:"allowed_origins"`
	AllowedMethods types.Set    `tfsdk:"allowed_methods"`
	AllowedHeaders types.Set    `tfsdk:"allowed_headers"`
	ExposeHeaders  types.Set    `tfsdk:"expose_headers"`
	MaxAgeSeconds  types.Int64  `tfsdk:"max_age_seconds"`
}

func newBucketCORS() resource.Resource { return &setting[corsModel]{spec: corsSpec()} }

func corsSpec() settingSpec[corsModel] {
	strSet := func(desc string, required bool, v ...validator.Set) schema.SetAttribute {
		// At least one value: MOS stores an empty list as none, so [] would plan a change every run.
		v = append(v, setvalidator.SizeAtLeast(1))
		return schema.SetAttribute{Description: desc, ElementType: types.StringType, Required: required, Optional: !required, Validators: v}
	}
	return settingSpec[corsModel]{
		typeName: "mos_bucket_cors",
		about: "Which web pages may call a MOS bucket from a browser (CORS). This resource holds all of the " +
			"bucket's rules; destroy removes them.",
		attributes: map[string]schema.Attribute{
			"rule": schema.SetNestedAttribute{
				Description: "The rules, 1 to 100. Their order has no meaning.",
				Required:    true,
				Validators:  []validator.Set{setvalidator.SizeBetween(1, 100)},
				NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
					"id": schema.StringAttribute{Description: "A name for the rule.", Optional: true, Validators: []validator.String{stringvalidator.LengthBetween(1, 255)}},
					"allowed_origins": strSet("Origins allowed, for example `https://app.example.com`, or `*` for any.", true,
						setvalidator.SizeAtLeast(1)),
					"allowed_methods": strSet("HTTP methods allowed: `GET`, `PUT`, `POST`, `DELETE` or `HEAD`.", true,
						setvalidator.SizeAtLeast(1), setvalidator.ValueStringsAre(stringvalidator.OneOf("GET", "PUT", "POST", "DELETE", "HEAD"))),
					"allowed_headers": strSet("Request headers allowed, or `*` for any.", false),
					"expose_headers":  strSet("Response headers the browser may read.", false),
					"max_age_seconds": schema.Int64Attribute{
						Description: "How long a browser may cache the answer to a preflight request, 1 to 86400 seconds.",
						Optional:    true, Validators: []validator.Int64{int64validator.Between(1, 86400)},
					},
				}},
			},
		},
		keys: func(m *corsModel) (*types.String, *types.String) { return &m.ID, &m.Bucket },
		read: func(ctx context.Context, c *client.Client, bucket string, m *corsModel, diags *diag.Diagnostics) {
			rules, err := c.GetBucketCORS(ctx, bucket)
			if err != nil {
				readError(diags, "mos_bucket_cors", err)
				return
			}
			m.Rules = make([]corsRuleModel, len(rules))
			for i, r := range rules {
				m.Rules[i] = corsRuleModel{
					ID: optString(r.ID), AllowedOrigins: setOf(r.AllowedOrigins), AllowedMethods: setOf(r.AllowedMethods),
					AllowedHeaders: setOf(r.AllowedHeaders), ExposeHeaders: setOf(r.ExposeHeaders), MaxAgeSeconds: optInt(r.MaxAgeSeconds),
				}
			}
		},
		write: func(ctx context.Context, c *client.Client, bucket string, m *corsModel) error {
			rules := make([]client.CORSRule, len(m.Rules))
			for i, r := range m.Rules {
				rules[i] = client.CORSRule{
					ID: r.ID.ValueString(), AllowedOrigins: strsOf(r.AllowedOrigins), AllowedMethods: strsOf(r.AllowedMethods),
					AllowedHeaders: strsOf(r.AllowedHeaders), ExposeHeaders: strsOf(r.ExposeHeaders), MaxAgeSeconds: r.MaxAgeSeconds.ValueInt64(),
				}
			}
			return c.PutBucketCORS(ctx, bucket, rules)
		},
		clear: func(ctx context.Context, c *client.Client, bucket string) error {
			return c.PutBucketCORS(ctx, bucket, nil)
		},
	}
}

// --- lifecycle

type lifecycleModel struct {
	ID     types.String         `tfsdk:"id"`
	Bucket types.String         `tfsdk:"bucket"`
	Rules  []lifecycleRuleModel `tfsdk:"rule"`
}

type lifecycleRuleModel struct {
	ID                 types.String `tfsdk:"id"`
	Prefix             types.String `tfsdk:"prefix"`
	Enabled            types.Bool   `tfsdk:"enabled"`
	ExpirationDays     types.Int64  `tfsdk:"expiration_days"`
	ExpirationDate     types.String `tfsdk:"expiration_date"`
	AbortIncompleteDay types.Int64  `tfsdk:"abort_incomplete_upload_days"`
}

func newBucketLifecycle() resource.Resource { return &setting[lifecycleModel]{spec: lifecycleSpec()} }

func lifecycleSpec() settingSpec[lifecycleModel] {
	days := func(desc string) schema.Int64Attribute {
		return schema.Int64Attribute{Description: desc, Optional: true, Validators: []validator.Int64{int64validator.Between(1, 36500)}}
	}
	return settingSpec[lifecycleModel]{
		typeName: "mos_bucket_lifecycle",
		about: "Rules that delete a MOS bucket's objects after some days or on a date, and clean up unfinished " +
			"uploads. This resource holds all of the bucket's rules; destroy removes them. MOS matches objects by " +
			"name prefix only (not by tag), and has no storage classes to move objects between.",
		attributes: map[string]schema.Attribute{
			"rule": schema.SetNestedAttribute{
				Description: "The rules, 1 to 1000. Their order has no meaning.",
				Required:    true,
				Validators:  []validator.Set{setvalidator.SizeBetween(1, 1000)},
				NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
					"id": schema.StringAttribute{Description: "The rule's name, unique in the bucket.", Required: true,
						Validators: []validator.String{stringvalidator.LengthBetween(1, 255)}},
					"prefix": schema.StringAttribute{Description: "The rule covers objects whose names start with this. Unset covers the whole bucket.",
						Optional: true, Validators: []validator.String{stringvalidator.LengthAtLeast(1)}},
					// Optional+Computed with a default: rules are normally on.
					"enabled":                      schema.BoolAttribute{Description: "Whether the rule runs. Defaults to `true`.", Optional: true, Computed: true, Default: booldefault.StaticBool(true)},
					"expiration_days":              days("Delete objects this many days after they were written."),
					"expiration_date":              schema.StringAttribute{Description: "Delete objects on this date (`YYYY-MM-DD`, at midnight UTC).", Optional: true},
					"abort_incomplete_upload_days": days("Abort uploads still unfinished this many days after they started, freeing their space."),
				}},
			},
		},
		keys: func(m *lifecycleModel) (*types.String, *types.String) { return &m.ID, &m.Bucket },
		validate: func(m *lifecycleModel, diags *diag.Diagnostics) {
			seen := map[string]bool{}
			for _, r := range m.Rules {
				if r.ID.IsUnknown() || r.ExpirationDate.IsUnknown() || r.ExpirationDays.IsUnknown() || r.AbortIncompleteDay.IsUnknown() {
					continue
				}
				id := r.ID.ValueString()
				p := path.Root("rule")
				switch {
				case seen[id]:
					diags.AddAttributeError(p, "Duplicate rule ID", fmt.Sprintf("Two rules are named %q. Give each rule its own id.", id))
				case !r.ExpirationDays.IsNull() && !r.ExpirationDate.IsNull():
					diags.AddAttributeError(p, "Conflicting expiry", fmt.Sprintf("Rule %q sets expiration_days and expiration_date. Use one.", id))
				case r.ExpirationDays.IsNull() && r.ExpirationDate.IsNull() && r.AbortIncompleteDay.IsNull():
					diags.AddAttributeError(p, "Rule does nothing", fmt.Sprintf("Rule %q sets no action. Set expiration_days, expiration_date or abort_incomplete_upload_days.", id))
				}
				if d := r.ExpirationDate.ValueString(); d != "" {
					if _, err := time.Parse("2006-01-02", d); err != nil {
						diags.AddAttributeError(p, "Invalid expiration_date", fmt.Sprintf("Rule %q: %q isn't a date as YYYY-MM-DD.", id, d))
					}
				}
				seen[id] = true
			}
		},
		read: func(ctx context.Context, c *client.Client, bucket string, m *lifecycleModel, diags *diag.Diagnostics) {
			rules, err := c.GetBucketLifecycle(ctx, bucket)
			if err != nil {
				readError(diags, "mos_bucket_lifecycle", err)
				return
			}
			m.Rules = make([]lifecycleRuleModel, len(rules))
			for i, r := range rules {
				m.Rules[i] = lifecycleRuleModel{
					ID: types.StringValue(r.ID), Prefix: optString(r.Prefix), Enabled: types.BoolValue(r.Enabled),
					ExpirationDays: optInt(r.ExpirationDays), ExpirationDate: optString(r.ExpirationDate), AbortIncompleteDay: optInt(r.AbortIncompleteUploadDays),
				}
			}
		},
		write: func(ctx context.Context, c *client.Client, bucket string, m *lifecycleModel) error {
			rules := make([]client.LifecycleRule, len(m.Rules))
			for i, r := range m.Rules {
				rules[i] = client.LifecycleRule{
					ID: r.ID.ValueString(), Prefix: r.Prefix.ValueString(), Enabled: r.Enabled.ValueBool(),
					ExpirationDays: r.ExpirationDays.ValueInt64(), ExpirationDate: r.ExpirationDate.ValueString(), AbortIncompleteUploadDays: r.AbortIncompleteDay.ValueInt64(),
				}
			}
			return c.PutBucketLifecycle(ctx, bucket, rules)
		},
		clear: func(ctx context.Context, c *client.Client, bucket string) error {
			return c.PutBucketLifecycle(ctx, bucket, nil)
		},
	}
}

// --- access logging

type loggingModel struct {
	ID           types.String `tfsdk:"id"`
	Bucket       types.String `tfsdk:"bucket"`
	TargetBucket types.String `tfsdk:"target_bucket"`
	TargetPrefix types.String `tfsdk:"target_prefix"`
}

func newBucketLogging() resource.Resource { return &setting[loggingModel]{spec: loggingSpec()} }

func loggingSpec() settingSpec[loggingModel] {
	return settingSpec[loggingModel]{
		typeName: "mos_bucket_logging",
		about: "Write a log of every request to a MOS bucket into another bucket. Destroy turns logging off; " +
			"the logs already written stay.",
		attributes: map[string]schema.Attribute{
			"target_bucket": schema.StringAttribute{
				Description: "The bucket the logs go to. Use another bucket: logging a bucket into itself writes a log for every log, without end.",
				Required:    true, Validators: bucketNameValidators(),
			},
			"target_prefix": schema.StringAttribute{Description: "Start every log's name with this, for example `logs/`.", Optional: true,
				Validators: []validator.String{stringvalidator.LengthAtLeast(1)}},
		},
		keys: func(m *loggingModel) (*types.String, *types.String) { return &m.ID, &m.Bucket },
		validate: func(m *loggingModel, diags *diag.Diagnostics) {
			if !m.Bucket.IsUnknown() && !m.TargetBucket.IsUnknown() && m.Bucket.ValueString() == m.TargetBucket.ValueString() {
				diags.AddAttributeWarning(path.Root("target_bucket"), "Bucket logs into itself",
					"Every log written adds a request to log, so the bucket fills with logs of logs. Send them to another bucket.")
			}
		},
		read: func(ctx context.Context, c *client.Client, bucket string, m *loggingModel, diags *diag.Diagnostics) {
			l, err := c.GetBucketLogging(ctx, bucket)
			if err != nil {
				readError(diags, "mos_bucket_logging", err)
				return
			}
			m.TargetBucket = types.StringValue(l.TargetBucket) // "" when turned off elsewhere: the plan turns it on again
			m.TargetPrefix = optString(l.TargetPrefix)
		},
		write: func(ctx context.Context, c *client.Client, bucket string, m *loggingModel) error {
			return c.PutBucketLogging(ctx, bucket, client.BucketLogging{TargetBucket: m.TargetBucket.ValueString(), TargetPrefix: m.TargetPrefix.ValueString()})
		},
		clear: func(ctx context.Context, c *client.Client, bucket string) error {
			return c.PutBucketLogging(ctx, bucket, client.BucketLogging{})
		},
	}
}

// --- object lock default retention

type objectLockModel struct {
	ID     types.String `tfsdk:"id"`
	Bucket types.String `tfsdk:"bucket"`
	Mode   types.String `tfsdk:"mode"`
	Days   types.Int64  `tfsdk:"days"`
	Years  types.Int64  `tfsdk:"years"`
}

func newBucketObjectLock() resource.Resource {
	return &setting[objectLockModel]{spec: objectLockSpec()}
}

func objectLockSpec() settingSpec[objectLockModel] {
	return settingSpec[objectLockModel]{
		typeName: "mos_bucket_object_lock",
		about: "Keep every new object in a MOS bucket from being deleted or overwritten for a set time. The bucket " +
			"must have been created with `object_lock_enabled = true`. Destroy removes the default; objects " +
			"already written keep their retention. Needs object lock to be enabled on MTN Object Storage.",
		attributes: map[string]schema.Attribute{
			// Optional+Computed with a default: MOS has one mode.
			"mode": schema.StringAttribute{
				Description: "`GOVERNANCE`: a user allowed to bypass it can still delete an object early (`mtncloud_mos_object`'s " +
					"`bypass_governance_on_destroy`). MOS has no other mode.",
				Optional: true, Computed: true, Default: stringdefault.StaticString("GOVERNANCE"),
				Validators: []validator.String{stringvalidator.OneOf("GOVERNANCE")},
			},
			"days": schema.Int64Attribute{Description: "Keep new objects this many days. Set `days` or `years`.", Optional: true,
				Validators: []validator.Int64{int64validator.Between(1, 36500), int64validator.ExactlyOneOf(path.MatchRoot("days"), path.MatchRoot("years"))}},
			"years": schema.Int64Attribute{Description: "Keep new objects this many years.", Optional: true,
				Validators: []validator.Int64{int64validator.Between(1, 100)}},
		},
		keys: func(m *objectLockModel) (*types.String, *types.String) { return &m.ID, &m.Bucket },
		read: func(ctx context.Context, c *client.Client, bucket string, m *objectLockModel, diags *diag.Diagnostics) {
			l, err := c.GetObjectLock(ctx, bucket)
			if err != nil {
				readError(diags, "mos_bucket_object_lock", err)
				return
			}
			m.Mode = types.StringValue(strings.ToUpper(l.Mode))
			if l.Mode == "" {
				m.Mode = types.StringValue("GOVERNANCE") // no default set (removed elsewhere): days/years show the drift
			}
			m.Days, m.Years = optInt(l.Days), optInt(l.Years)
		},
		write: func(ctx context.Context, c *client.Client, bucket string, m *objectLockModel) error {
			return c.PutObjectLock(ctx, bucket, client.ObjectLockDefault{Mode: m.Mode.ValueString(), Days: m.Days.ValueInt64(), Years: m.Years.ValueInt64()})
		},
		clear: func(ctx context.Context, c *client.Client, bucket string) error {
			return c.PutObjectLock(ctx, bucket, client.ObjectLockDefault{})
		},
	}
}
