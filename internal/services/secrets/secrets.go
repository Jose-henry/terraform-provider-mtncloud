// Package secrets holds the Cypher resources whose value MTN Cloud generates: a password, a UUID
// and an AES key. One spec per mount, one engine. Keys you set yourself (secret/, tfvars/) are the
// inherited mtncloud_cypher_secret and mtncloud_cypher_tfvars.
package secrets

import (
	"context"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64default"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/mtn/terraform-provider-mtncloud/internal/client"
)

type mountSpec struct {
	typeName string // resource type, without the provider prefix
	mount    string // password, uuid or key
	what     string
	size     string // the size attribute ("length", "bits"), or "" when the mount has none
	sizeDesc string
	sizeDef  int64
	sizeOK   validator.Int64
}

func mountSpecs() []mountSpec {
	return []mountSpec{
		{typeName: "cypher_password", mount: "password", what: "password", size: "length", sizeDef: 15,
			sizeDesc: "Number of characters, with symbols, digits, upper and lower case letters. Defaults to 15.",
			sizeOK:   int64validator.Between(8, 256)},
		{typeName: "cypher_uuid", mount: "uuid", what: "UUID"},
		{typeName: "cypher_key", mount: "key", what: "Base64 AES key", size: "bits", sizeDef: 256,
			sizeDesc: "Key length in bits: 128, 192 or 256. Defaults to 256.",
			sizeOK:   int64validator.OneOf(128, 192, 256)},
	}
}

// Resources lists every resource in this package, for the provider to register.
func Resources() []func() resource.Resource {
	out := make([]func() resource.Resource, 0, len(mountSpecs()))
	for _, s := range mountSpecs() {
		out = append(out, func() resource.Resource { return &generated{spec: s} })
	}
	return out
}

var (
	_ resource.ResourceWithConfigure   = (*generated)(nil)
	_ resource.ResourceWithImportState = (*generated)(nil)
)

// generated is a Cypher key whose value MTN Cloud generates.
type generated struct {
	client *client.Client
	spec   mountSpec
}

// model is read and written attribute by attribute: the size attribute exists on some mounts only.
type model struct {
	ID, Name, TTL, Value types.String
	Size                 types.Int64
}

var ttlFormat = regexp.MustCompile(`^(0|[1-9][0-9]*[smhdwy]?)$`)

func (r *generated) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_" + r.spec.typeName
}

func (r *generated) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	s := r.spec
	replace := []planmodifier.String{stringplanmodifier.RequiresReplace()}
	attrs := map[string]schema.Attribute{
		"id": schema.StringAttribute{
			Computed: true, Description: fmt.Sprintf("The Cypher key as stored, for example `%s/db-admin`.", s.mount),
			PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
		},
		"name": schema.StringAttribute{
			Required:      true,
			Description:   fmt.Sprintf("Key name, without the `%s/` mount, for example `db-admin`. Changing it makes a new %s.", s.mount, s.what),
			PlanModifiers: replace,
			Validators: []validator.String{stringvalidator.RegexMatches(regexp.MustCompile(`^[A-Za-z0-9._-]+(/[A-Za-z0-9._-]+)*$`),
				"letters, digits, `.`, `_` and `-`, in `/`-separated parts")},
		},
		"ttl": schema.StringAttribute{
			Optional: true,
			Description: fmt.Sprintf("Lease: how long the %s lives before MTN Cloud deletes it, in seconds or as `15m`, `8h`, `7d`. "+
				"Leave it out, or `0`, to keep it for good. Changing it makes a new %s.", s.what, s.what),
			// The lease isn't read back: after an import, state has none, and that isn't a change.
			PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplaceIf(
				func(_ context.Context, req planmodifier.StringRequest, resp *stringplanmodifier.RequiresReplaceIfFuncResponse) {
					resp.RequiresReplace = !req.StateValue.IsNull()
				}, "Changing the lease makes a new key.", "Changing the lease makes a new key.")},
			Validators: []validator.String{stringvalidator.RegexMatches(ttlFormat, "seconds, or a number followed by s, m, h, d, w or y")},
		},
		"value": schema.StringAttribute{
			Computed: true, Sensitive: true,
			Description:   fmt.Sprintf("The generated %s. It is stored in the Terraform state (marked sensitive): protect the state.", s.what),
			PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
		},
	}
	if s.size != "" {
		// Optional+Computed with a default: left out, the default size is used and shown.
		attrs[s.size] = schema.Int64Attribute{
			Optional: true, Computed: true, Default: int64default.StaticInt64(s.sizeDef),
			Description:   s.sizeDesc + " Changing it makes a new " + s.what + ".",
			PlanModifiers: []planmodifier.Int64{int64planmodifier.RequiresReplace()},
			Validators:    []validator.Int64{s.sizeOK},
		}
	}
	resp.Schema = schema.Schema{
		Description: fmt.Sprintf("A %s that MTN Cloud generates and keeps in Cypher, its secret store, under `%s/`. "+
			"Workflows read it with `<%%=cypher.read('%s/<name>')%%>`. Destroying it deletes the key.", s.what, s.mount, s.mount),
		Attributes: attrs,
	}
}

func (r *generated) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	c, ok := req.ProviderData.(*client.Client)
	if !ok {
		resp.Diagnostics.AddError("Unexpected provider data", fmt.Sprintf("expected *client.Client, got %T. This is a provider bug.", req.ProviderData))
		return
	}
	r.client = c
}

func (r *generated) ready(diags *diag.Diagnostics) bool {
	if r.client == nil {
		diags.AddError("MTN Cloud provider is not configured yet",
			"The provider block uses values that are only known after apply. Use values known at plan time, or apply the resources they come from first.")
		return false
	}
	return true
}

// get reads the model from a plan or state.
func (r *generated) get(ctx context.Context, g interface {
	GetAttribute(context.Context, path.Path, any) diag.Diagnostics
}, diags *diag.Diagnostics) model {
	var m model
	for name, dst := range map[string]*types.String{"id": &m.ID, "name": &m.Name, "ttl": &m.TTL, "value": &m.Value} {
		diags.Append(g.GetAttribute(ctx, path.Root(name), dst)...)
	}
	if r.spec.size != "" {
		diags.Append(g.GetAttribute(ctx, path.Root(r.spec.size), &m.Size)...)
	}
	return m
}

func (r *generated) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	if !r.ready(&resp.Diagnostics) {
		return
	}
	m := r.get(ctx, req.Plan, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	// The size goes in the key pattern: password/15/db-admin, key/256/app.
	asked := r.spec.mount + "/" + m.Name.ValueString()
	short := asked
	if r.spec.size != "" {
		asked = r.spec.mount + "/" + strconv.FormatInt(m.Size.ValueInt64(), 10) + "/" + m.Name.ValueString()
	}
	// Never overwrite: writing an existing key generates a new value, which would break whatever uses it.
	for _, k := range []string{asked} {
		exists, _, err := r.client.CypherKeyExists(ctx, k)
		if err != nil {
			resp.Diagnostics.AddError("MTN Cloud API error", "Unable to check Cypher for an existing key: "+err.Error())
			return
		}
		if exists {
			resp.Diagnostics.AddAttributeError(path.Root("name"), "Cypher key already exists",
				fmt.Sprintf("%s is already in Cypher. Import it with `terraform import <address> %s`, or choose another name.", k, k))
			return
		}
	}
	ttl := m.TTL.ValueString()
	if ttl == "" {
		ttl = "0" // no lease: left out, the platform's default lease would expire the key
	}
	if err := r.client.WriteCypher(ctx, asked, "", ttl); err != nil {
		resp.Diagnostics.AddError("MTN Cloud API error", "Unable to create the Cypher key: "+err.Error())
		return
	}
	// The platform may store the key with or without the size part: use what it stored.
	stored := ""
	for _, k := range slices.Compact([]string{asked, short}) {
		exists, _, err := r.client.CypherKeyExists(ctx, k)
		if err != nil {
			resp.Diagnostics.AddError("MTN Cloud API error", "Unable to find the new Cypher key: "+err.Error())
			return
		}
		if exists {
			stored = k
			break
		}
	}
	if stored == "" {
		resp.Diagnostics.AddError("Cypher key not found after create",
			fmt.Sprintf("MTN Cloud accepted %s but doesn't list it. Check Cypher in the console before applying again.", asked))
		return
	}
	secret, err := r.client.ReadCypher(ctx, stored) // exists, so this reads and doesn't create
	if err != nil {
		resp.Diagnostics.AddError("MTN Cloud API error", "Unable to read the new Cypher key: "+err.Error())
		return
	}
	m.ID, m.Value = types.StringValue(stored), types.StringValue(secret.Value)
	resp.State.Raw = req.Plan.Raw
	r.set(ctx, &resp.State, m, &resp.Diagnostics)
}

func (r *generated) set(ctx context.Context, st interface {
	SetAttribute(context.Context, path.Path, any) diag.Diagnostics
}, m model, diags *diag.Diagnostics) {
	for name, v := range map[string]types.String{"id": m.ID, "name": m.Name, "ttl": m.TTL, "value": m.Value} {
		diags.Append(st.SetAttribute(ctx, path.Root(name), v)...)
	}
	if r.spec.size != "" {
		diags.Append(st.SetAttribute(ctx, path.Root(r.spec.size), m.Size)...)
	}
}

func (r *generated) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	if !r.ready(&resp.Diagnostics) {
		return
	}
	m := r.get(ctx, req.State, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	key := m.ID.ValueString()
	// A read of a missing key would create a new one: look it up in the list first.
	exists, _, err := r.client.CypherKeyExists(ctx, key)
	if err != nil {
		resp.Diagnostics.AddError("MTN Cloud API error", "Unable to check the Cypher key: "+err.Error())
		return
	}
	if !exists {
		resp.State.RemoveResource(ctx) // deleted or expired: plan a new one (rule 8)
		return
	}
	secret, err := r.client.ReadCypher(ctx, key)
	if err != nil {
		resp.Diagnostics.AddError("MTN Cloud API error", "Unable to read the Cypher key: "+err.Error())
		return
	}
	m.Value = types.StringValue(secret.Value)
	if !m.Name.IsNull() {
		r.set(ctx, &resp.State, m, &resp.Diagnostics)
		return
	}
	// After an import, name and size come from the key: <mount>[/<size>]/<name>.
	rest := strings.TrimPrefix(key, r.spec.mount+"/")
	if r.spec.size != "" {
		if first, after, ok := strings.Cut(rest, "/"); ok {
			if n, err := strconv.ParseInt(first, 10, 64); err == nil {
				m.Size, rest = types.Int64Value(n), after
			}
		}
		if m.Size.IsNull() {
			m.Size = types.Int64Value(r.spec.sizeDef)
		}
	}
	m.Name = types.StringValue(rest)
	r.set(ctx, &resp.State, m, &resp.Diagnostics)
}

// Update never runs: every argument replaces the key.
func (r *generated) Update(_ context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	resp.State.Raw = req.Plan.Raw
}

func (r *generated) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	if !r.ready(&resp.Diagnostics) {
		return
	}
	var id types.String
	resp.Diagnostics.Append(req.State.GetAttribute(ctx, path.Root("id"), &id)...)
	if err := r.client.DeleteCypher(ctx, id.ValueString()); err != nil {
		resp.Diagnostics.AddError("MTN Cloud API error", "Unable to delete the Cypher key: "+err.Error())
	}
}

func (r *generated) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	if !strings.HasPrefix(req.ID, r.spec.mount+"/") {
		resp.Diagnostics.AddError("Invalid import ID",
			fmt.Sprintf("Import a %s by its Cypher key, starting with %s/ (mtncloud_cyphers lists them), not %q.", r.spec.what, r.spec.mount, req.ID))
		return
	}
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}
