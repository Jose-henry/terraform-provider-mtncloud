package storage

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-framework-timeouts/resource/timeouts"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/boolplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/mtn/terraform-provider-mtncloud/internal/client"
)

var (
	_ resource.ResourceWithConfigure   = (*bucketResource)(nil)
	_ resource.ResourceWithImportState = (*bucketResource)(nil)
)

func newBucket() resource.Resource { return &bucketResource{} }

type bucketResource struct{ base }

type bucketModel struct {
	ID           types.String   `tfsdk:"id"`
	Name         types.String   `tfsdk:"name"`
	NamePrefix   types.String   `tfsdk:"name_prefix"`
	ConsoleName  types.String   `tfsdk:"console_name"`
	Endpoint     types.String   `tfsdk:"endpoint"`
	EndpointURL  types.String   `tfsdk:"endpoint_url"`
	ObjectLock   types.Bool     `tfsdk:"object_lock_enabled"`
	ForceDestroy types.Bool     `tfsdk:"force_destroy"`
	Timeouts     timeouts.Value `tfsdk:"timeouts"`
}

func (r *bucketResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_mos_bucket"
}

func (r *bucketResource) Schema(ctx context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "A bucket in MTN Object Storage (MOS), registered in the console under Infrastructure > Storage > Buckets " +
			"so it can be used there too. The provider uses the tenant's MOS keys from Cypher: order \"MTN Object Storage (MOS)\" " +
			"from the catalog once before using it. Destroy deletes the bucket in MOS and its console record. It refuses while " +
			"an archive uses the bucket (deleting the record would delete the archive), and while the bucket holds objects " +
			"unless `force_destroy` is set.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Description:   "The console bucket record's ID.",
				Computed:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			// Optional+Computed: written by the user, or made from name_prefix when the bucket is created.
			"name": schema.StringAttribute{
				Description: "The bucket's name in MOS: 3 to 63 lower-case letters, digits, dots and hyphens. Names are " +
					"shared by every tenant on MOS, so a name another tenant uses is refused: `name_prefix` avoids that. " +
					"Set `name` or `name_prefix`, not both. Changing it replaces the bucket, and its objects are lost.",
				Optional:   true,
				Computed:   true,
				Validators: append(bucketNameValidators(), stringvalidator.ExactlyOneOf(path.MatchRoot("name_prefix"))),
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
					stringplanmodifier.RequiresReplaceIfConfigured(),
				},
			},
			"name_prefix": schema.StringAttribute{
				Description: "Make a unique name that starts with this prefix: the prefix and " + strconv.Itoa(suffixLen) + " random " +
					"lower-case letters and digits, chosen when the bucket is created and kept from then on. Up to " +
					strconv.Itoa(63-suffixLen) + " characters. Changing it replaces the bucket, and its objects are lost.",
				Optional:   true,
				Validators: []validator.String{stringvalidator.LengthBetween(1, 63-suffixLen), prefixValidator{}},
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplaceIf(prefixChanged,
					"Replaces the bucket, unless its name already starts with the prefix.",
					"Replaces the bucket, unless its name already starts with the prefix.")},
			},
			// Optional+Computed: the user may name the console record; otherwise it takes the bucket's name.
			"console_name": schema.StringAttribute{
				Description:   "The bucket's name in the console. Defaults to `name` when the bucket is created; removing it later keeps the current console name. Changing it renames the record in place.",
				Optional:      true,
				Computed:      true,
				Validators:    []validator.String{stringvalidator.LengthBetween(1, 255)},
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			// Optional+Computed with a default: most users never set it.
			"endpoint": schema.StringAttribute{
				Description: "Which MOS endpoint the console record uses: `public` (the default, reachable from anywhere) " +
					"or `private` (from inside MTN Cloud only). Changing it updates the record in place.",
				Optional:   true,
				Computed:   true,
				Default:    stringdefault.StaticString("public"),
				Validators: []validator.String{stringvalidator.OneOf("public", "private")},
			},
			"endpoint_url": schema.StringAttribute{
				Description: "The endpoint's URL, for S3-compatible tools (path-style addressing).",
				Computed:    true,
			},
			// Optional+Computed with a default, so imported buckets read their real setting.
			"object_lock_enabled": schema.BoolAttribute{
				Description: "Create the bucket with object lock, so objects can be kept from deletion for a retention " +
					"period (`mtncloud_mos_bucket_object_lock`, `mtncloud_mos_object`). It can only be set when the " +
					"bucket is created, and never turned off: changing it replaces the bucket. Needs object lock to be " +
					"enabled on MTN Object Storage; the create fails with a clear message when it isn't.",
				Optional:      true,
				Computed:      true,
				Default:       booldefault.StaticBool(false),
				PlanModifiers: []planmodifier.Bool{boolplanmodifier.RequiresReplace()},
			},
			// Optional+Computed with a default: a Terraform-only setting, never sent to the API.
			"force_destroy": schema.BoolAttribute{
				Description: "On destroy, delete every object in the bucket first. Without it, destroying a bucket that " +
					"holds objects fails. Objects under retention can't be deleted until it ends.",
				Optional: true,
				Computed: true,
				Default:  booldefault.StaticBool(false),
			},
			"timeouts": timeouts.Attributes(ctx, timeouts.Opts{Create: true, Update: true, Delete: true}),
		},
	}
}

// endpointURL picks the URL for a public or private endpoint.
func endpointURL(cr *client.MOSCredentials, which string) (string, error) {
	switch {
	case which == "private" && cr.PrivateEndpoint != "":
		return cr.PrivateEndpoint, nil
	case which == "private":
		return "", fmt.Errorf("the tenant's MOS keys come with no private endpoint (they are read from %s). Use endpoint = \"public\"", cr.Source)
	case cr.PublicEndpoint != "":
		return cr.PublicEndpoint, nil
	}
	return "", fmt.Errorf("the tenant's MOS keys come with no public endpoint (they are read from %s)", cr.Source)
}

// endpointKind tells which endpoint a record's URL is, or "" when it is neither.
func endpointKind(cr *client.MOSCredentials, url string) string {
	same := func(a, b string) bool {
		return a != "" && strings.EqualFold(strings.TrimRight(a, "/"), strings.TrimRight(b, "/"))
	}
	switch {
	case same(cr.PublicEndpoint, url):
		return "public"
	case same(cr.PrivateEndpoint, url):
		return "private"
	}
	return ""
}

func (r *bucketResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	if !r.ready(&resp.Diagnostics) {
		return
	}
	var plan bucketModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	timeout, d := plan.Timeouts.Create(ctx, mosTimeout)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	generated := plan.Name.IsUnknown() || plan.Name.IsNull()
	if generated {
		plan.Name = types.StringValue(prefixedName(plan.NamePrefix.ValueString()))
	}
	name := plan.Name.ValueString()
	consoleDefault := plan.ConsoleName.IsUnknown() || plan.ConsoleName.IsNull()
	if consoleDefault {
		plan.ConsoleName = plan.Name
	}
	cr, err := r.client.MOSCredentials(ctx)
	if err != nil {
		resp.Diagnostics.AddError("MTN Object Storage isn't available", err.Error())
		return
	}
	ep, err := endpointURL(cr, plan.Endpoint.ValueString())
	if err != nil {
		resp.Diagnostics.AddAttributeError(path.Root("endpoint"), "Endpoint not available", err.Error())
		return
	}
	in := client.StorageBucketInput{Name: plan.ConsoleName.ValueString(), BucketName: name, Endpoint: ep}

	// A bucket that exists already is never adopted: it may hold someone's data. Import it instead.
	exists, err := r.client.MOSBucketExists(ctx, name)
	for try := 0; generated && try < 3 && (exists || client.IsMOSCode(err, "BucketAlreadyExists")); try++ {
		name = prefixedName(plan.NamePrefix.ValueString()) // a made-up name that is taken: make another
		plan.Name = types.StringValue(name)
		in.BucketName = name
		if consoleDefault {
			plan.ConsoleName, in.Name = plan.Name, name
		}
		exists, err = r.client.MOSBucketExists(ctx, name)
	}
	if err != nil {
		resp.Diagnostics.AddError("Unable to check for an existing bucket", err.Error())
		return
	}
	if exists {
		resp.Diagnostics.AddAttributeError(path.Root("name"), "Bucket already exists",
			fmt.Sprintf("Your tenant already has a bucket named %q. Import it with `terraform import <address> %s`, or choose another name.", name, name))
		return
	}

	// MOS first, then the console record: the console's own create checks a node that may not see
	// the new bucket yet, and fails, leaving the bucket behind (dev-docs/api-research.md).
	if err := r.client.CreateMOSBucket(ctx, name, plan.ObjectLock.ValueBool()); err != nil {
		// It didn't exist a moment ago, so whatever is there now is ours and empty: remove it, or the
		// next apply finds a bucket that can be neither created nor imported (it has no record).
		if !client.IsMOSCode(err, "BucketAlreadyExists") {
			err = r.removeNew(ctx, name, err)
		}
		resp.Diagnostics.AddError("Unable to create bucket", err.Error())
		return
	}
	id, err := r.register(ctx, in)
	if err != nil {
		// The bucket is new and empty: remove it, so the next apply starts clean instead of finding it.
		resp.Diagnostics.AddError("Unable to register the bucket in the console", r.removeNew(ctx, name, err).Error())
		return
	}
	plan.ID = types.StringValue(strconv.FormatInt(id, 10))
	plan.EndpointURL = types.StringValue(in.Endpoint)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// register adds the console record. A record for this bucket name already there can only be a stale
// one (the bucket didn't exist in MOS a moment ago) or ours from a lost answer (rule 11): reuse it.
func (r *bucketResource) register(ctx context.Context, in client.StorageBucketInput) (int64, error) {
	found, err := r.client.FindStorageBuckets(ctx, in.BucketName)
	if err != nil {
		return 0, err
	}
	if len(found) > 0 {
		id := found[0].ID
		return id, r.client.UpdateStorageBucket(ctx, id, in)
	}
	return r.client.CreateStorageBucket(ctx, in)
}

func (r *bucketResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	if !r.ready(&resp.Diagnostics) {
		return
	}
	var state bucketModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	gone := r.refresh(ctx, &state, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	if gone {
		resp.State.RemoveResource(ctx) // deleted outside Terraform: plan a new one
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// refresh reads the record and the bucket into m, and reports whether either is gone.
func (r *bucketResource) refresh(ctx context.Context, m *bucketModel, diags *diag.Diagnostics) bool {
	id, err := strconv.ParseInt(m.ID.ValueString(), 10, 64)
	if err != nil {
		diags.AddError("Invalid bucket ID", fmt.Sprintf("%q is not a console bucket record ID: %s", m.ID.ValueString(), err))
		return false
	}
	rec, err := r.client.GetStorageBucket(ctx, id)
	if client.IsNotFound(err) {
		return true
	}
	if err != nil {
		diags.AddError("Unable to read the bucket's console record", err.Error())
		return false
	}
	exists, err := r.client.MOSBucketExists(ctx, rec.BucketName)
	if err != nil {
		diags.AddError("Unable to read the bucket", err.Error())
		return false
	}
	if !exists {
		diags.AddWarning("Bucket deleted outside Terraform",
			fmt.Sprintf("MOS bucket %q is gone, but its console record %d is still there. Applying again creates the bucket and reuses the record; "+
				"if you are removing the bucket instead, delete the record in the console (Infrastructure > Storage > Buckets).", rec.BucketName, id))
		return true
	}
	cr, err := r.client.MOSCredentials(ctx)
	if err != nil {
		diags.AddError("MTN Object Storage isn't available", err.Error())
		return false
	}
	m.Name = types.StringValue(rec.BucketName)
	m.ConsoleName = types.StringValue(rec.Name)
	m.EndpointURL = types.StringValue(rec.Endpoint)
	// A record pointing anywhere else (changed in the console) reads as "", so the plan puts it back.
	m.Endpoint = types.StringValue(endpointKind(cr, rec.Endpoint))
	// Object lock is read only where it can be on: buckets without it answer an error on clusters
	// where the feature is off, and the setting can't change after create anyway.
	if m.ObjectLock.IsNull() || m.ObjectLock.IsUnknown() || m.ObjectLock.ValueBool() {
		lock, err := r.client.GetObjectLock(ctx, rec.BucketName)
		switch {
		case err == nil:
			m.ObjectLock = types.BoolValue(lock.Enabled)
		case m.ObjectLock.IsNull() || m.ObjectLock.IsUnknown():
			m.ObjectLock = types.BoolValue(false) // an import: the cluster can't hold a lock it can't read
		default:
			diags.AddError("Unable to read the bucket's object lock", err.Error())
			return false
		}
	}
	if m.ForceDestroy.IsNull() || m.ForceDestroy.IsUnknown() {
		m.ForceDestroy = types.BoolValue(false)
	}
	return false
}

func (r *bucketResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	if !r.ready(&resp.Diagnostics) {
		return
	}
	var plan, state bucketModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	timeout, d := plan.Timeouts.Update(ctx, mosTimeout)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	plan.ID = state.ID
	if plan.ConsoleName.IsUnknown() || plan.ConsoleName.IsNull() {
		plan.ConsoleName = state.ConsoleName
	}
	plan.EndpointURL = state.EndpointURL
	if !plan.ConsoleName.Equal(state.ConsoleName) || !plan.Endpoint.Equal(state.Endpoint) {
		cr, err := r.client.MOSCredentials(ctx)
		if err != nil {
			resp.Diagnostics.AddError("MTN Object Storage isn't available", err.Error())
			return
		}
		id, _ := strconv.ParseInt(state.ID.ValueString(), 10, 64)
		ep, err := endpointURL(cr, plan.Endpoint.ValueString())
		if err != nil {
			resp.Diagnostics.AddAttributeError(path.Root("endpoint"), "Endpoint not available", err.Error())
			return
		}
		in := client.StorageBucketInput{Name: plan.ConsoleName.ValueString(), BucketName: plan.Name.ValueString(), Endpoint: ep}
		if err := r.client.UpdateStorageBucket(ctx, id, in); err != nil {
			resp.Diagnostics.AddError("Unable to update the bucket's console record", err.Error())
			return
		}
		plan.EndpointURL = types.StringValue(in.Endpoint)
	}
	// force_destroy and timeouts live in state only.
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *bucketResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	if !r.ready(&resp.Diagnostics) {
		return
	}
	var state bucketModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	timeout, d := state.Timeouts.Delete(ctx, mosTimeout)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	id, _ := strconv.ParseInt(state.ID.ValueString(), 10, 64)
	name := state.Name.ValueString()

	// Deleting the record deletes every archive built on it, without a word: never do that here.
	archives, err := r.client.ArchivesOn(ctx, id)
	if err != nil {
		resp.Diagnostics.AddError("Unable to check the bucket's archives", err.Error())
		return
	}
	if len(archives) > 0 {
		resp.Diagnostics.AddError("Bucket is used by an archive",
			fmt.Sprintf("Archive %s stores its files in bucket %q. Deleting the bucket would delete the archive too. "+
				"Delete or move the archive in the console (Tools > Archives) first, then destroy again.", quoteAll(archives), name))
		return
	}
	if state.ForceDestroy.ValueBool() {
		if err := r.client.EmptyMOSBucket(ctx, name); err != nil {
			resp.Diagnostics.AddError("Unable to empty the bucket", err.Error())
			return
		}
	}
	if err := r.client.DeleteMOSBucket(ctx, name); err != nil {
		resp.Diagnostics.AddError("Unable to delete the bucket", err.Error())
		return
	}
	// The console never deletes the bucket in MOS, so the record goes last.
	if err := r.client.DeleteStorageBucket(ctx, id); err != nil {
		resp.Diagnostics.AddError("Unable to delete the bucket's console record",
			fmt.Sprintf("The bucket %q is deleted from MOS, but its console record %d isn't: %s. Delete it in the console.", name, id, err))
	}
}

// removeNew deletes a bucket this create made, after cause stopped the create. It returns cause,
// with what to do when the bucket couldn't be removed.
func (r *bucketResource) removeNew(ctx context.Context, name string, cause error) error {
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
	defer cancel()
	exists, err := r.client.MOSBucketExists(cleanup, name)
	if err == nil && exists {
		err = r.client.DeleteMOSBucket(cleanup, name)
	}
	if err != nil {
		return fmt.Errorf("%w; removing the new bucket %q from MOS afterwards also failed (%w): register it in the console "+
			"(Infrastructure > Storage > Buckets) and import it, or delete it with an S3-compatible tool", cause, name, err)
	}
	return cause
}

// ImportState takes the console record ID, or the bucket's name.
func (r *bucketResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	if !r.ready(&resp.Diagnostics) {
		return
	}
	// A record ID, unless no record has it and it is also a bucket name (bucket names may be digits).
	if id, err := strconv.ParseInt(req.ID, 10, 64); err == nil {
		_, gerr := r.client.GetStorageBucket(ctx, id)
		if gerr == nil || !client.IsNotFound(gerr) || bucketNameProblem(req.ID) != "" {
			resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
			return
		}
	}
	if p := bucketNameProblem(req.ID); p != "" {
		resp.Diagnostics.AddError("Invalid import ID", fmt.Sprintf("Import a bucket by its console record ID or its name. %q isn't either: %s.", req.ID, p))
		return
	}
	found, err := r.client.FindStorageBuckets(ctx, req.ID)
	switch {
	case err != nil:
		resp.Diagnostics.AddError("Unable to find the bucket", err.Error())
	case len(found) == 0:
		resp.Diagnostics.AddError("Bucket isn't in the console",
			fmt.Sprintf("No console bucket record uses MOS bucket %q. Register it in the console (Infrastructure > Storage > Buckets) first, then import it.", req.ID))
	case len(found) > 1:
		ids := make([]string, len(found))
		for i, b := range found {
			ids[i] = strconv.FormatInt(b.ID, 10)
		}
		resp.Diagnostics.AddError("More than one console record",
			fmt.Sprintf("Records %s all use bucket %q. Import one by its ID.", strings.Join(ids, ", "), req.ID))
	default:
		resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), strconv.FormatInt(found[0].ID, 10))...)
	}
}

func quoteAll(names []string) string {
	q := make([]string, len(names))
	for i, n := range names {
		q[i] = strconv.Quote(n)
	}
	return strings.Join(q, ", ")
}
