package storage

import (
	"context"
	"fmt"
	"strconv"

	"github.com/hashicorp/terraform-plugin-framework-validators/resourcevalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/mtn/terraform-provider-mtncloud/internal/client"
)

var (
	_ resource.ResourceWithConfigure        = (*archiveResource)(nil)
	_ resource.ResourceWithImportState      = (*archiveResource)(nil)
	_ resource.ResourceWithModifyPlan       = (*archiveResource)(nil)
	_ resource.ResourceWithConfigValidators = (*archiveResource)(nil)
)

func newArchive() resource.Resource { return &archiveResource{} }

// archiveResource is an archive (Tools > Archives): a console file store kept in a MOS bucket.
type archiveResource struct{ base }

type archiveModel struct {
	ID           types.String `tfsdk:"id"`
	Name         types.String `tfsdk:"name"`
	Description  types.String `tfsdk:"description"`
	MOSBucket    types.String `tfsdk:"mos_bucket"`
	MOSBucketID  types.String `tfsdk:"mos_bucket_id"`
	PublicURL    types.Bool   `tfsdk:"public_url"`
	ForceDestroy types.Bool   `tfsdk:"force_destroy"`
	FileCount    types.Int64  `tfsdk:"file_count"`
	SizeBytes    types.Int64  `tfsdk:"size_bytes"`
}

func (r *archiveResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_archive_bucket"
}

func (r *archiveResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	keep := []planmodifier.String{stringplanmodifier.UseStateForUnknown()}
	resp.Schema = schema.Schema{
		Description: "An archive (Tools > Archives in the console): a file store for your tenant whose files are kept in a MOS " +
			"bucket. Destroy deletes the archive and every file in it, also from the MOS bucket; it refuses while the archive " +
			"holds files unless `force_destroy` is set.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{Description: "The archive's ID.", Computed: true, PlanModifiers: keep},
			"name": schema.StringAttribute{
				Description: "The archive's name. Names are unique across every tenant on MTN Cloud. Changing it renames the archive in place.",
				Required:    true,
				Validators:  []validator.String{stringvalidator.LengthBetween(1, 255)},
			},
			// Optional+Computed with a default, so taking it out of the configuration clears it.
			"description": schema.StringAttribute{Description: "A description.", Optional: true, Computed: true, Default: stringdefault.StaticString("")},
			// Optional+Computed: give the bucket by name or by ID; the other is filled in.
			"mos_bucket": schema.StringAttribute{
				Description: "The MOS bucket the files are kept in, by its name in MOS (`mtncloud_mos_bucket.x.name`). It must be " +
					"registered in the console (Infrastructure > Storage > Buckets), as `mtncloud_mos_bucket` does. Set this or " +
					"`mos_bucket_id`. Changing the bucket replaces the archive, and its files are lost.",
				Optional: true, Computed: true,
				Validators: bucketNameValidators(),
			},
			"mos_bucket_id": schema.StringAttribute{
				Description: "The MOS bucket by its console record ID (`mtncloud_mos_bucket.x.id`). Set this or `mos_bucket`. " +
					"Changing the bucket replaces the archive, and its files are lost.",
				Optional: true, Computed: true,
				Validators: []validator.String{stringvalidator.RegexMatches(digits, "must be a console bucket record ID (digits)")},
			},
			// Optional+Computed with a default: archives are private unless asked.
			"public_url": schema.BoolAttribute{
				Description: "Let anyone download the archive's files from their public URL, **without logging in**. Defaults to `false`.",
				Optional:    true, Computed: true, Default: booldefault.StaticBool(false),
			},
			// Optional+Computed with a default: a Terraform-only setting, never sent to the API.
			"force_destroy": schema.BoolAttribute{
				Description: "Destroy the archive even while it holds files. They are deleted with it, also from the MOS bucket.",
				Optional:    true, Computed: true, Default: booldefault.StaticBool(false),
			},
			"file_count": schema.Int64Attribute{Description: "How many files the archive holds.", Computed: true},
			"size_bytes": schema.Int64Attribute{Description: "The total size of its files, in bytes.", Computed: true},
		},
	}
}

func (r *archiveResource) ConfigValidators(context.Context) []resource.ConfigValidator {
	return []resource.ConfigValidator{resourcevalidator.ExactlyOneOf(path.MatchRoot("mos_bucket"), path.MatchRoot("mos_bucket_id"))}
}

// ModifyPlan resolves the MOS bucket the user named into both its name and its record ID, at plan
// time (D-006), and replaces the archive when that is a different bucket. A bucket created in the
// same run isn't registered yet: then the ID is resolved when the archive is created.
func (r *archiveResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.Plan.Raw.IsNull() || r.client == nil {
		return
	}
	var cfg, plan archiveModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	plan.MOSBucket, plan.MOSBucketID = cfg.MOSBucket, cfg.MOSBucketID
	switch {
	case !cfg.MOSBucketID.IsNull() && !cfg.MOSBucketID.IsUnknown():
		plan.MOSBucket = types.StringUnknown()
		id, _ := strconv.ParseInt(cfg.MOSBucketID.ValueString(), 10, 64)
		rec, err := r.client.GetStorageBucket(ctx, id)
		switch {
		case client.IsNotFound(err):
			resp.Diagnostics.AddAttributeError(path.Root("mos_bucket_id"), "No such MOS bucket",
				fmt.Sprintf("No console bucket record has ID %d. Use `mtncloud_mos_bucket.<name>.id`, or see Infrastructure > Storage > Buckets.", id))
			return
		case err != nil:
			resp.Diagnostics.AddAttributeError(path.Root("mos_bucket_id"), "Unable to read the MOS bucket", err.Error())
			return
		}
		plan.MOSBucket = types.StringValue(rec.BucketName)
	case !cfg.MOSBucket.IsNull() && !cfg.MOSBucket.IsUnknown():
		plan.MOSBucketID = types.StringUnknown()
		found, err := r.client.FindStorageBuckets(ctx, cfg.MOSBucket.ValueString())
		switch {
		case err != nil:
			resp.Diagnostics.AddAttributeError(path.Root("mos_bucket"), "Unable to look up the MOS bucket", err.Error())
			return
		case len(found) > 1:
			resp.Diagnostics.AddAttributeError(path.Root("mos_bucket"), "More than one console record",
				fmt.Sprintf("Several console bucket records use MOS bucket %q. Set mos_bucket_id to the one to use.", cfg.MOSBucket.ValueString()))
			return
		case len(found) == 1:
			plan.MOSBucketID = types.StringValue(strconv.FormatInt(found[0].ID, 10))
		}
	default:
		plan.MOSBucket, plan.MOSBucketID = types.StringUnknown(), types.StringUnknown()
	}
	if !req.State.Raw.IsNull() {
		var state archiveModel
		resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
		if resp.Diagnostics.HasError() {
			return
		}
		if plan.MOSBucketID.IsUnknown() || !plan.MOSBucketID.Equal(state.MOSBucketID) {
			resp.RequiresReplace = append(resp.RequiresReplace, path.Root("mos_bucket_id"))
		}
		// file_count and size_bytes stay unknown on an update: people add files in the console
		// at any time, so the counts after apply can differ from the ones in state.
	}
	resp.Diagnostics.Append(resp.Plan.Set(ctx, &plan)...)
}

// storageID finds the console record for the archive's bucket at apply time, for a bucket that was
// registered after the plan was made.
func (r *archiveResource) storageID(ctx context.Context, m *archiveModel, diags *diag.Diagnostics) int64 {
	if !m.MOSBucketID.IsUnknown() && !m.MOSBucketID.IsNull() {
		id, _ := strconv.ParseInt(m.MOSBucketID.ValueString(), 10, 64)
		return id
	}
	found, err := r.client.FindStorageBuckets(ctx, m.MOSBucket.ValueString())
	switch {
	case err != nil:
		diags.AddAttributeError(path.Root("mos_bucket"), "Unable to look up the MOS bucket", err.Error())
	case len(found) == 0:
		diags.AddAttributeError(path.Root("mos_bucket"), "MOS bucket isn't in the console",
			fmt.Sprintf("No console bucket record uses MOS bucket %q. Create the bucket with mtncloud_mos_bucket, or register it in the console "+
				"(Infrastructure > Storage > Buckets).", m.MOSBucket.ValueString()))
	case len(found) > 1:
		diags.AddAttributeError(path.Root("mos_bucket"), "More than one console record",
			fmt.Sprintf("Several console bucket records use MOS bucket %q. Set mos_bucket_id to the one to use.", m.MOSBucket.ValueString()))
	default:
		return found[0].ID
	}
	return 0
}

func (r *archiveResource) fill(ctx context.Context, m *archiveModel, a *client.ArchiveBucket, diags *diag.Diagnostics) {
	m.ID = types.StringValue(strconv.FormatInt(a.ID, 10))
	m.Name, m.Description = types.StringValue(a.Name), types.StringValue(a.Description)
	m.PublicURL = types.BoolValue(a.IsPublic)
	m.FileCount, m.SizeBytes = types.Int64Value(a.FileCount), types.Int64Value(a.RawSize)
	id := strconv.FormatInt(a.StorageBucketID, 10)
	if m.MOSBucketID.ValueString() != id || m.MOSBucket.IsNull() || m.MOSBucket.IsUnknown() {
		// The archive answers with the record's console name; the MOS bucket name is on the record.
		rec, err := r.client.GetStorageBucket(ctx, a.StorageBucketID)
		switch {
		case client.IsNotFound(err):
			m.MOSBucket = types.StringValue("")
		case err != nil:
			diags.AddError("Unable to read the archive's MOS bucket", err.Error())
			return
		default:
			m.MOSBucket = types.StringValue(rec.BucketName)
		}
	}
	m.MOSBucketID = types.StringValue(id)
	if m.ForceDestroy.IsNull() || m.ForceDestroy.IsUnknown() {
		m.ForceDestroy = types.BoolValue(false)
	}
}

func (r *archiveResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	if !r.ready(&resp.Diagnostics) {
		return
	}
	var plan archiveModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	sid := r.storageID(ctx, &plan, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	name := plan.Name.ValueString()
	// Names are unique across tenants: say so plainly instead of passing on the API's refusal.
	if found, err := r.client.FindArchiveBucket(ctx, name); err != nil {
		resp.Diagnostics.AddError("Unable to check for an existing archive", err.Error())
		return
	} else if found != nil {
		resp.Diagnostics.AddAttributeError(path.Root("name"), "Archive already exists",
			fmt.Sprintf("An archive named %q already exists (ID %d, owner %q), and archive names are unique across all tenants. If it is your tenant's, import it with `terraform import <address> %d`; otherwise choose another name.", name, found.ID, found.Owner, found.ID))
		return
	}
	in := client.ArchiveBucketInput{Name: name, Description: plan.Description.ValueString(), StorageBucketID: sid, IsPublic: plan.PublicURL.ValueBool()}
	a, err := r.client.CreateArchiveBucket(ctx, in)
	if err != nil && client.MayHaveSucceeded(err) {
		// The answer was lost: an archive with this name on this bucket now is the one just made (rule 11).
		if found, ferr := r.client.FindArchiveBucket(ctx, name); ferr == nil && found != nil && found.StorageBucketID == sid {
			a, err = found, nil
		}
	}
	if err != nil {
		resp.Diagnostics.AddError("Unable to create the archive", err.Error())
		return
	}
	r.fill(ctx, &plan, a, &resp.Diagnostics)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *archiveResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	if !r.ready(&resp.Diagnostics) {
		return
	}
	var state archiveModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	id, err := strconv.ParseInt(state.ID.ValueString(), 10, 64)
	if err != nil {
		resp.Diagnostics.AddError("Invalid archive ID", fmt.Sprintf("%q is not an archive ID: %s", state.ID.ValueString(), err))
		return
	}
	a, err := r.client.GetArchiveBucket(ctx, id)
	if client.IsNotFound(err) {
		resp.State.RemoveResource(ctx) // deleted outside Terraform: plan a new one
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Unable to read the archive", err.Error())
		return
	}
	r.fill(ctx, &state, a, &resp.Diagnostics)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *archiveResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	if !r.ready(&resp.Diagnostics) {
		return
	}
	var plan, state archiveModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	plan.ID = state.ID
	id, _ := strconv.ParseInt(state.ID.ValueString(), 10, 64)
	if !plan.Name.Equal(state.Name) || !plan.Description.Equal(state.Description) || !plan.PublicURL.Equal(state.PublicURL) {
		in := client.ArchiveBucketInput{Name: plan.Name.ValueString(), Description: plan.Description.ValueString(), IsPublic: plan.PublicURL.ValueBool()}
		if err := r.client.UpdateArchiveBucket(ctx, id, in); err != nil {
			resp.Diagnostics.AddError("Unable to update the archive", err.Error())
			return
		}
	}
	a, err := r.client.GetArchiveBucket(ctx, id)
	if err != nil {
		resp.Diagnostics.AddError("Unable to read the archive after the change", err.Error())
		return
	}
	r.fill(ctx, &plan, a, &resp.Diagnostics)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *archiveResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	if !r.ready(&resp.Diagnostics) {
		return
	}
	var state archiveModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	id, _ := strconv.ParseInt(state.ID.ValueString(), 10, 64)
	a, err := r.client.GetArchiveBucket(ctx, id)
	if client.IsNotFound(err) {
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Unable to read the archive", err.Error())
		return
	}
	// The API deletes an archive with its files, from the MOS bucket too (checked live, 2026-10-29).
	if a.FileCount > 0 && !state.ForceDestroy.ValueBool() {
		resp.Diagnostics.AddError("Archive holds files",
			fmt.Sprintf("Archive %q holds %d file(s). Deleting it deletes them too, also from the MOS bucket. Set force_destroy = true "+
				"and apply, then destroy again, or delete the files first (Tools > Archives).", a.Name, a.FileCount))
		return
	}
	if err := r.client.DeleteArchiveBucket(ctx, id); err != nil {
		resp.Diagnostics.AddError("Unable to delete the archive", err.Error())
	}
}

// ImportState takes the archive's ID or its name.
func (r *archiveResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	if !r.ready(&resp.Diagnostics) {
		return
	}
	// All digits is an ID, unless no archive has that ID: then it is a name (an archive named "2026").
	if id, err := strconv.ParseInt(req.ID, 10, 64); err == nil {
		_, gerr := r.client.GetArchiveBucket(ctx, id)
		if gerr == nil {
			resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
			return
		}
		if !client.IsNotFound(gerr) {
			resp.Diagnostics.AddError("Unable to read the archive", gerr.Error())
			return
		}
	}
	found, err := r.client.FindArchiveBucket(ctx, req.ID)
	switch {
	case err != nil:
		resp.Diagnostics.AddError("Unable to find the archive", err.Error())
	case found == nil:
		resp.Diagnostics.AddError("No such archive", fmt.Sprintf("Your tenant has no archive named %q. Import one by its ID or name (Tools > Archives).", req.ID))
	default:
		resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), strconv.FormatInt(found.ID, 10))...)
	}
}
