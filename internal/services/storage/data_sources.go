package storage

import (
	"context"
	"encoding/base64"
	"fmt"
	"unicode/utf8"

	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	ds "github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	rs "github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/mtn/terraform-provider-mtncloud/internal/client"
)

// computed turns a resource attribute into the same attribute, read-only, for a data source: the
// data sources describe a setting with the resource's own names, types and descriptions.
func computed(a rs.Attribute) ds.Attribute {
	switch a := a.(type) {
	case rs.StringAttribute:
		return ds.StringAttribute{Description: a.Description, Computed: true}
	case rs.Int64Attribute:
		return ds.Int64Attribute{Description: a.Description, Computed: true}
	case rs.BoolAttribute:
		return ds.BoolAttribute{Description: a.Description, Computed: true}
	case rs.SetAttribute:
		return ds.SetAttribute{Description: a.Description, ElementType: a.ElementType, Computed: true}
	case rs.ListAttribute:
		return ds.ListAttribute{Description: a.Description, ElementType: a.ElementType, Computed: true}
	case rs.MapAttribute:
		return ds.MapAttribute{Description: a.Description, ElementType: a.ElementType, Computed: true}
	case rs.SetNestedAttribute:
		return ds.SetNestedAttribute{Description: a.Description, Computed: true, NestedObject: ds.NestedAttributeObject{Attributes: computedAll(a.NestedObject.Attributes)}}
	}
	panic(fmt.Sprintf("storage: no data source form for %T", a)) // a schema bug, caught by the provider tests
}

func computedAll(in map[string]rs.Attribute) map[string]ds.Attribute {
	out := make(map[string]ds.Attribute, len(in))
	for k, a := range in {
		out[k] = computed(a)
	}
	return out
}

func bucketArg() ds.StringAttribute {
	return ds.StringAttribute{Description: "The bucket's name.", Required: true, Validators: bucketNameValidators()}
}

// --- one data source per bucket setting, from the resource's spec

type settingData[T any] struct {
	dataBase
	spec settingSpec[T]
}

func (d *settingData[T]) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_" + d.spec.typeName
}

func (d *settingData[T]) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	attrs := computedAll(d.spec.attributes)
	attrs["id"] = ds.StringAttribute{Description: "The bucket's name.", Computed: true}
	attrs["bucket"] = bucketArg()
	resp.Schema = ds.Schema{Description: "Reads the " + what(d.spec.typeName) + " of a MOS bucket.", Attributes: attrs}
}

func (d *settingData[T]) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	if !d.ready(&resp.Diagnostics) {
		return
	}
	var m T
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	id, bucket := d.spec.keys(&m)
	var diags diag.Diagnostics
	d.spec.read(ctx, d.client, bucket.ValueString(), &m, &diags)
	if bucketGoneIn(diags) {
		resp.Diagnostics.AddAttributeError(rootBucket, "No such bucket", fmt.Sprintf("Your tenant has no bucket named %q. mtncloud_mos_buckets lists them.", bucket.ValueString()))
		return
	}
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	*id = *bucket
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

func newBucketACLData() datasource.DataSource { return &settingData[aclModel]{spec: aclSpec()} }
func newBucketCORSData() datasource.DataSource {
	return &settingData[corsModel]{spec: emptyOK(corsSpec(), func(m *corsModel) { m.Rules = []corsRuleModel{} })}
}

func newBucketLifecycleData() datasource.DataSource {
	return &settingData[lifecycleModel]{spec: emptyOK(lifecycleSpec(), func(m *lifecycleModel) { m.Rules = []lifecycleRuleModel{} })}
}
func newBucketLoggingData() datasource.DataSource {
	return &settingData[loggingModel]{spec: emptyOK(loggingSpec(), noLogging)}
}
func newBucketObjectLockData() datasource.DataSource {
	return &settingData[objectLockModel]{spec: objectLockSpec()}
}

func noLogging(m *loggingModel) {
	m.TargetBucket, m.TargetPrefix = types.StringNull(), types.StringNull()
}

// emptyOK makes a setting MOS doesn't support read as empty in a data source, not as an error.
func emptyOK[T any](s settingSpec[T], empty func(*T)) settingSpec[T] {
	read := s.read
	s.read = func(ctx context.Context, c *client.Client, bucket string, m *T, diags *diag.Diagnostics) {
		var d diag.Diagnostics
		read(ctx, c, bucket, m, &d)
		if d.HasError() && !bucketGoneIn(d) && notSupported(d) {
			empty(m)
			return
		}
		diags.Append(d...)
	}
	return s
}

func notSupported(d diag.Diagnostics) bool {
	for _, x := range d {
		if x.Severity() == diag.SeverityError && containsAny(x.Detail(), "doesn't support this") {
			return true
		}
	}
	return false
}

// --- credentials

type credentialsData struct{ dataBase }

type credentialsModel struct {
	ID              types.String `tfsdk:"id"`
	AccessKey       types.String `tfsdk:"access_key"`
	SecretKey       types.String `tfsdk:"secret_key"`
	PublicEndpoint  types.String `tfsdk:"public_endpoint"`
	PrivateEndpoint types.String `tfsdk:"private_endpoint"`
	Source          types.String `tfsdk:"source"`
}

func newCredentials() datasource.DataSource { return &credentialsData{} }

func (d *credentialsData) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_mos_credentials"
}

func (d *credentialsData) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = ds.Schema{
		Description: "The tenant's MOS keys and endpoints, for S3-compatible tools and applications. The keys are " +
			"stored in Terraform state (marked sensitive): protect the state. Ordering MOS again from the catalog " +
			"replaces the keys for the whole tenant.",
		Attributes: map[string]ds.Attribute{
			"id":               ds.StringAttribute{Description: "Always `mos`.", Computed: true},
			"access_key":       ds.StringAttribute{Description: "The access key ID.", Computed: true, Sensitive: true},
			"secret_key":       ds.StringAttribute{Description: "The secret access key.", Computed: true, Sensitive: true},
			"public_endpoint":  ds.StringAttribute{Description: "The endpoint reachable from anywhere (path-style).", Computed: true},
			"private_endpoint": ds.StringAttribute{Description: "The endpoint reachable from inside MTN Cloud, when there is one.", Computed: true},
			"source":           ds.StringAttribute{Description: "Where the keys came from: `cypher` or `environment`.", Computed: true},
		},
	}
}

func (d *credentialsData) Read(ctx context.Context, _ datasource.ReadRequest, resp *datasource.ReadResponse) {
	if !d.ready(&resp.Diagnostics) {
		return
	}
	cr, err := d.client.MOSCredentials(ctx)
	if err != nil {
		resp.Diagnostics.AddError("MTN Object Storage isn't available", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &credentialsModel{
		ID: types.StringValue("mos"), AccessKey: types.StringValue(cr.AccessKey), SecretKey: types.StringValue(cr.SecretKey),
		PublicEndpoint: types.StringValue(cr.PublicEndpoint), PrivateEndpoint: types.StringValue(cr.PrivateEndpoint), Source: types.StringValue(cr.Source),
	})...)
}

// --- buckets

type bucketsData struct{ dataBase }

type bucketRow struct {
	Name         types.String `tfsdk:"name"`
	CreationDate types.String `tfsdk:"creation_date"`
	ConsoleID    types.String `tfsdk:"console_id"`
	ConsoleName  types.String `tfsdk:"console_name"`
}

type bucketsModel struct {
	ID      types.String `tfsdk:"id"`
	Buckets []bucketRow  `tfsdk:"buckets"`
}

func newBuckets() datasource.DataSource { return &bucketsData{} }

func (d *bucketsData) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_mos_buckets"
}

func bucketRowAttributes() map[string]ds.Attribute {
	return map[string]ds.Attribute{
		"name":          ds.StringAttribute{Description: "The bucket's name in MOS.", Computed: true},
		"creation_date": ds.StringAttribute{Description: "When it was created (RFC 3339).", Computed: true},
		"console_id":    ds.StringAttribute{Description: "Its console bucket record's ID (the `mtncloud_mos_bucket` ID), or null when it isn't in the console.", Computed: true},
		"console_name":  ds.StringAttribute{Description: "Its name in the console, or null.", Computed: true},
	}
}

func (d *bucketsData) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = ds.Schema{
		Description: "Lists the tenant's MOS buckets, sorted by name, with their console records.",
		Attributes: map[string]ds.Attribute{
			"id":      ds.StringAttribute{Description: "Always `mos-buckets`.", Computed: true},
			"buckets": ds.ListNestedAttribute{Description: "The buckets.", Computed: true, NestedObject: ds.NestedAttributeObject{Attributes: bucketRowAttributes()}},
		},
	}
}

func (d *bucketsData) Read(ctx context.Context, _ datasource.ReadRequest, resp *datasource.ReadResponse) {
	if !d.ready(&resp.Diagnostics) {
		return
	}
	buckets, err := d.client.ListMOSBuckets(ctx)
	if err != nil {
		resp.Diagnostics.AddError("Unable to list buckets", err.Error())
		return
	}
	records, err := d.client.ListStorageBuckets(ctx)
	if err != nil {
		resp.Diagnostics.AddError("Unable to list the console's bucket records", err.Error())
		return
	}
	byName := map[string]*client.StorageBucket{}
	for _, r := range records {
		if _, dup := byName[r.BucketName]; !dup {
			byName[r.BucketName] = r
		}
	}
	m := bucketsModel{ID: types.StringValue("mos-buckets"), Buckets: make([]bucketRow, 0, len(buckets))}
	for _, b := range sortedBuckets(buckets) {
		row := bucketRow{Name: types.StringValue(b.Name), CreationDate: types.StringValue(b.CreationDate), ConsoleID: types.StringNull(), ConsoleName: types.StringNull()}
		if r := byName[b.Name]; r != nil {
			row.ConsoleID, row.ConsoleName = types.StringValue(fmt.Sprint(r.ID)), types.StringValue(r.Name)
		}
		m.Buckets = append(m.Buckets, row)
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

// --- one bucket, all of it

type bucketData struct{ dataBase }

type bucketDataModel struct {
	ID                types.String         `tfsdk:"id"`
	Name              types.String         `tfsdk:"name"`
	CreationDate      types.String         `tfsdk:"creation_date"`
	ConsoleID         types.String         `tfsdk:"console_id"`
	ConsoleName       types.String         `tfsdk:"console_name"`
	EndpointURL       types.String         `tfsdk:"endpoint_url"`
	ObjectLockEnabled types.Bool           `tfsdk:"object_lock_enabled"`
	ACL               types.String         `tfsdk:"acl"`
	Grants            types.List           `tfsdk:"grants"`
	CORSRules         []corsRuleModel      `tfsdk:"cors_rule"`
	LifecycleRules    []lifecycleRuleModel `tfsdk:"lifecycle_rule"`
	LoggingBucket     types.String         `tfsdk:"logging_target_bucket"`
	LoggingPrefix     types.String         `tfsdk:"logging_target_prefix"`
	RetentionMode     types.String         `tfsdk:"default_retention_mode"`
	RetentionDays     types.Int64          `tfsdk:"default_retention_days"`
	RetentionYears    types.Int64          `tfsdk:"default_retention_years"`
}

func newBucketData() datasource.DataSource { return &bucketData{} }

func (d *bucketData) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_mos_bucket"
}

func (d *bucketData) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	str := func(desc string) ds.StringAttribute { return ds.StringAttribute{Description: desc, Computed: true} }
	num := func(desc string) ds.Int64Attribute { return ds.Int64Attribute{Description: desc, Computed: true} }
	attrs := map[string]ds.Attribute{
		"id":                      str("The bucket's name."),
		"name":                    ds.StringAttribute{Description: "The bucket's name in MOS.", Required: true, Validators: bucketNameValidators()},
		"creation_date":           str("When it was created (RFC 3339)."),
		"console_id":              str("Its console bucket record's ID (the `mtncloud_mos_bucket` ID), or null when it isn't in the console."),
		"console_name":            str("Its name in the console, or null."),
		"endpoint_url":            str("The endpoint its console record uses, or null."),
		"object_lock_enabled":     ds.BoolAttribute{Description: "Whether the bucket was created with object lock.", Computed: true},
		"acl":                     computed(aclSpec().attributes["acl"]),
		"grants":                  computed(aclSpec().attributes["grants"]),
		"cors_rule":               computed(corsSpec().attributes["rule"]),
		"lifecycle_rule":          computed(lifecycleSpec().attributes["rule"]),
		"logging_target_bucket":   str("Where its access logs go, or null when logging is off."),
		"logging_target_prefix":   str("The prefix of its access logs' names, or null."),
		"default_retention_mode":  str("The default retention for new objects (`GOVERNANCE`), or null."),
		"default_retention_days":  num("Days new objects are kept, or null."),
		"default_retention_years": num("Years new objects are kept, or null."),
	}
	resp.Schema = ds.Schema{
		Description: "Reads one MOS bucket and all its settings: console record, ACL, CORS and lifecycle rules, " +
			"access logging and object lock. A setting MOS doesn't support reads as empty.",
		Attributes: attrs,
	}
}

func (d *bucketData) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	if !d.ready(&resp.Diagnostics) {
		return
	}
	var m bucketDataModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	name := m.Name.ValueString()
	buckets, err := d.client.ListMOSBuckets(ctx)
	if err != nil {
		resp.Diagnostics.AddError("Unable to list buckets", err.Error())
		return
	}
	found := false
	for _, b := range buckets {
		if b.Name == name {
			found, m.CreationDate = true, types.StringValue(b.CreationDate)
		}
	}
	if !found {
		resp.Diagnostics.AddAttributeError(rootName, "No such bucket", fmt.Sprintf("Your tenant has no bucket named %q. mtncloud_mos_buckets lists them.", name))
		return
	}
	m.ID = m.Name
	m.ConsoleID, m.ConsoleName, m.EndpointURL = types.StringNull(), types.StringNull(), types.StringNull()
	recs, err := d.client.FindStorageBuckets(ctx, name)
	if err != nil {
		resp.Diagnostics.AddError("Unable to read the bucket's console record", err.Error())
		return
	}
	if len(recs) > 0 {
		m.ConsoleID, m.ConsoleName, m.EndpointURL = types.StringValue(fmt.Sprint(recs[0].ID)), types.StringValue(recs[0].Name), types.StringValue(recs[0].Endpoint)
	}

	var diags diag.Diagnostics
	var acl aclModel
	aclSpec().read(ctx, d.client, name, &acl, &diags)
	m.ACL, m.Grants = acl.ACL, acl.Grants
	if acl.Grants.IsNull() {
		m.Grants = types.ListNull(types.StringType)
	}
	var cors corsModel
	emptyOK(corsSpec(), func(m *corsModel) { m.Rules = []corsRuleModel{} }).read(ctx, d.client, name, &cors, &diags)
	m.CORSRules = cors.Rules
	var lc lifecycleModel
	emptyOK(lifecycleSpec(), func(m *lifecycleModel) { m.Rules = []lifecycleRuleModel{} }).read(ctx, d.client, name, &lc, &diags)
	m.LifecycleRules = lc.Rules
	var lg loggingModel
	emptyOK(loggingSpec(), noLogging).read(ctx, d.client, name, &lg, &diags)
	m.LoggingBucket, m.LoggingPrefix = optString(lg.TargetBucket.ValueString()), lg.TargetPrefix
	// A bucket without object lock may answer an error here on a cluster with the feature off.
	lock, err := d.client.GetObjectLock(ctx, name)
	m.ObjectLockEnabled = types.BoolValue(err == nil && lock.Enabled)
	m.RetentionMode, m.RetentionDays, m.RetentionYears = optString(lock.Mode), optInt(lock.Days), optInt(lock.Years)
	if err != nil {
		m.RetentionMode, m.RetentionDays, m.RetentionYears = types.StringNull(), types.Int64Null(), types.Int64Null()
	}
	resp.Diagnostics.Append(diags...)
	if !resp.Diagnostics.HasError() {
		resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
	}
}

// --- objects

type objectsData struct{ dataBase }

type objectRow struct {
	Key          types.String `tfsdk:"key"`
	ETag         types.String `tfsdk:"etag"`
	Size         types.Int64  `tfsdk:"size"`
	LastModified types.String `tfsdk:"last_modified"`
}

type objectsModel struct {
	ID        types.String `tfsdk:"id"`
	Bucket    types.String `tfsdk:"bucket"`
	Prefix    types.String `tfsdk:"prefix"`
	Delimiter types.String `tfsdk:"delimiter"`
	MaxKeys   types.Int64  `tfsdk:"max_keys"`
	Objects   []objectRow  `tfsdk:"objects"`
	Prefixes  types.List   `tfsdk:"common_prefixes"`
}

func newObjects() datasource.DataSource { return &objectsData{} }

func (d *objectsData) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_mos_objects"
}

func (d *objectsData) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	str := func(desc string) ds.StringAttribute { return ds.StringAttribute{Description: desc, Computed: true} }
	resp.Schema = ds.Schema{
		Description: "Lists the objects in a MOS bucket, in name order.",
		Attributes: map[string]ds.Attribute{
			"id":     str("`<bucket>/<prefix>`."),
			"bucket": bucketArg(),
			"prefix": ds.StringAttribute{Description: "Only objects whose names start with this.", Optional: true},
			"delimiter": ds.StringAttribute{Description: "Group names by this character, usually `/`: objects \"in a folder\" are left out, and " +
				"the folders are listed in `common_prefixes`.", Optional: true, Validators: []validator.String{stringvalidator.LengthBetween(1, 1)}},
			"max_keys": ds.Int64Attribute{Description: "At most this many objects, 1 to 100000. Defaults to 1000.", Optional: true,
				Validators: []validator.Int64{int64validator.Between(1, 100000)}},
			"objects": ds.ListNestedAttribute{Description: "The objects.", Computed: true, NestedObject: ds.NestedAttributeObject{Attributes: map[string]ds.Attribute{
				"key": str("The object's name."), "etag": str("Its ETag."), "size": ds.Int64Attribute{Description: "Its size in bytes.", Computed: true},
				"last_modified": str("When it was last written (RFC 3339)."),
			}}},
			"common_prefixes": ds.ListAttribute{Description: "The folders under `prefix`, when `delimiter` is set.", ElementType: types.StringType, Computed: true},
		},
	}
}

func (d *objectsData) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	if !d.ready(&resp.Diagnostics) {
		return
	}
	var m objectsModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	limit := 1000
	if !m.MaxKeys.IsNull() {
		limit = int(m.MaxKeys.ValueInt64())
	}
	l, err := d.client.ListMOSObjects(ctx, m.Bucket.ValueString(), m.Prefix.ValueString(), m.Delimiter.ValueString(), limit)
	if bucketGone(err) {
		resp.Diagnostics.AddAttributeError(rootBucket, "No such bucket", err.Error())
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Unable to list objects", err.Error())
		return
	}
	m.ID = types.StringValue(m.Bucket.ValueString() + "/" + m.Prefix.ValueString())
	m.Objects = make([]objectRow, len(l.Objects))
	for i, o := range l.Objects {
		m.Objects[i] = objectRow{Key: types.StringValue(o.Key), ETag: types.StringValue(o.ETag), Size: types.Int64Value(o.Size), LastModified: types.StringValue(o.LastModified)}
	}
	m.Prefixes, _ = types.ListValueFrom(ctx, types.StringType, l.Prefixes)
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

// --- one object

type objectData struct{ dataBase }

type objectDataModel struct {
	ID             types.String `tfsdk:"id"`
	Bucket         types.String `tfsdk:"bucket"`
	Key            types.String `tfsdk:"key"`
	IncludeContent types.Bool   `tfsdk:"include_content"`
	ETag           types.String `tfsdk:"etag"`
	Size           types.Int64  `tfsdk:"size"`
	LastModified   types.String `tfsdk:"last_modified"`
	Metadata       types.Map    `tfsdk:"metadata"`
	RetentionMode  types.String `tfsdk:"retention_mode"`
	RetainUntil    types.String `tfsdk:"retain_until"`
	ACL            types.String `tfsdk:"acl"`
	Content        types.String `tfsdk:"content"`
	ContentBase64  types.String `tfsdk:"content_base64"`
}

func newObjectData() datasource.DataSource { return &objectData{} }

func (d *objectData) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_mos_object"
}

func (d *objectData) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	str := func(desc string) ds.StringAttribute { return ds.StringAttribute{Description: desc, Computed: true} }
	resp.Schema = ds.Schema{
		Description: "Reads one object in a MOS bucket: its size, ETag, metadata, retention and ACL, and its content when asked.",
		Attributes: map[string]ds.Attribute{
			"id":     str("`<bucket>/<key>`."),
			"bucket": bucketArg(),
			"key":    ds.StringAttribute{Description: "The object's name.", Required: true, Validators: []validator.String{stringvalidator.LengthBetween(1, 1024)}},
			"include_content": ds.BoolAttribute{Description: "Also read the content, for objects up to 1 MiB. Defaults to `false`. " +
				"The content is stored in Terraform state.", Optional: true},
			"etag":           str("The ETag."),
			"size":           ds.Int64Attribute{Description: "The size in bytes.", Computed: true},
			"last_modified":  str("When it was last written (RFC 3339)."),
			"metadata":       ds.MapAttribute{Description: "Its user metadata.", ElementType: types.StringType, Computed: true},
			"retention_mode": str("`GOVERNANCE` when retention holds it, or null."),
			"retain_until":   str("When its retention ends (RFC 3339), or null."),
			"acl":            str("Its canned ACL, or `custom`."),
			"content":        str("The content as text, when `include_content` is set and it is valid UTF-8."),
			"content_base64": str("The content, base64-encoded, when `include_content` is set."),
		},
	}
}

func (d *objectData) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	if !d.ready(&resp.Diagnostics) {
		return
	}
	var m objectDataModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	bucket, key := m.Bucket.ValueString(), m.Key.ValueString()
	o, err := d.client.HeadMOSObject(ctx, bucket, key)
	if client.IsMOSNotFound(err) {
		resp.Diagnostics.AddAttributeError(rootKey, "No such object", fmt.Sprintf("Bucket %q has no object %q (or the bucket doesn't exist). mtncloud_mos_objects lists them.", bucket, key))
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Unable to read the object", err.Error())
		return
	}
	m.ID = types.StringValue(bucket + "/" + key)
	m.ETag, m.Size, m.LastModified = types.StringValue(o.ETag), types.Int64Value(o.Size), types.StringValue(o.LastModified)
	m.Metadata, _ = types.MapValueFrom(ctx, types.StringType, o.Metadata)
	m.RetentionMode, m.RetainUntil = optString(o.LockMode), optString(o.RetainUntil)
	a, err := d.client.GetObjectACL(ctx, bucket, key)
	if err != nil {
		resp.Diagnostics.AddError("Unable to read the object's ACL", err.Error())
		return
	}
	m.ACL = types.StringValue(a.Canned)
	m.Content, m.ContentBase64 = types.StringNull(), types.StringNull()
	if m.IncludeContent.ValueBool() {
		b, err := d.client.ReadMOSObject(ctx, bucket, key)
		if err != nil {
			resp.Diagnostics.AddError("Unable to read the object's content", err.Error())
			return
		}
		m.ContentBase64 = types.StringValue(base64.StdEncoding.EncodeToString(b))
		if utf8.Valid(b) {
			m.Content = types.StringValue(string(b))
		}
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

// --- object ACL

type objectACLData struct{ dataBase }

func newObjectACLData() datasource.DataSource { return &objectACLData{} }

func (d *objectACLData) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_mos_object_acl"
}

func (d *objectACLData) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = ds.Schema{
		Description: "Reads who may read one object in a MOS bucket.",
		Attributes: map[string]ds.Attribute{
			"id":     ds.StringAttribute{Description: "`<bucket>/<key>`.", Computed: true},
			"bucket": bucketArg(),
			"key":    ds.StringAttribute{Description: "The object's name.", Required: true},
			"acl":    ds.StringAttribute{Description: "Its canned ACL, or `custom` when it matches none.", Computed: true},
			"grants": ds.ListAttribute{Description: "The grants MOS holds, as `<grantee>:<permission>`.", ElementType: types.StringType, Computed: true},
		},
	}
}

func (d *objectACLData) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	if !d.ready(&resp.Diagnostics) {
		return
	}
	var m objectACLModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	a, err := d.client.GetObjectACL(ctx, m.Bucket.ValueString(), m.Key.ValueString())
	if client.IsMOSNotFound(err) {
		resp.Diagnostics.AddAttributeError(rootKey, "No such object", err.Error())
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Unable to read the object ACL", err.Error())
		return
	}
	m.ID = types.StringValue(m.Bucket.ValueString() + "/" + m.Key.ValueString())
	m.ACL = types.StringValue(a.Canned)
	m.Grants, _ = types.ListValueFrom(ctx, types.StringType, a.Grants)
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}
