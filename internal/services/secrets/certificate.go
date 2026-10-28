package secrets

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"time"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/mtn/terraform-provider-mtncloud/internal/client"
)

// certificate is an internal SSL certificate: a certificate and private key you upload. MTN Cloud
// never returns the certificate or key text, so the certificate and chain in state are what was
// last sent, and the key is write-only.
type certificate struct{ base }

func newCertificate() resource.Resource { return &certificate{} }

type certModel struct {
	ID                types.String `tfsdk:"id"`
	Name              types.String `tfsdk:"name"`
	Description       types.String `tfsdk:"description"`
	DomainName        types.String `tfsdk:"domain_name"`
	Wildcard          types.Bool   `tfsdk:"wildcard"`
	Certificate       types.String `tfsdk:"certificate"`
	Chain             types.String `tfsdk:"chain"`
	PrivateKey        types.String `tfsdk:"private_key"`
	PrivateKeyVersion types.Int64  `tfsdk:"private_key_version"`
	CommonName        types.String `tfsdk:"common_name"`
	SelfSigned        types.Bool   `tfsdk:"self_signed"`
}

func (r *certificate) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_ssl_certificate"
}

var (
	pemCert = regexp.MustCompile(`-----BEGIN CERTIFICATE-----`)
	pemKey  = regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----`)
)

func (r *certificate) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	keep := []planmodifier.String{stringplanmodifier.UseStateForUnknown()}
	resp.Schema = schema.Schema{
		Description: "An SSL certificate you upload: a certificate and its private key, for load balancers and apps. " +
			"MTN Cloud never returns the certificate or key, so changes to them made in the console aren't seen.",
		Attributes: map[string]schema.Attribute{
			"id":          schema.StringAttribute{Computed: true, Description: "Certificate ID.", PlanModifiers: keep},
			"name":        schema.StringAttribute{Required: true, Description: "Certificate name.", Validators: []validator.String{stringvalidator.LengthAtLeast(1)}},
			"description": schema.StringAttribute{Optional: true, Description: "Description."},
			"domain_name": schema.StringAttribute{Optional: true, Description: "Domain name the certificate is for."},
			// Optional+Computed: the default false is what MTN Cloud stores when it isn't sent.
			"wildcard": schema.BoolAttribute{Optional: true, Computed: true, Default: booldefault.StaticBool(false),
				Description: "Whether it is a wildcard certificate. Defaults to `false`."},
			"certificate": schema.StringAttribute{Required: true, Description: "The certificate, PEM-encoded. Changing it uploads it again " +
				"in place; a new certificate usually needs its new key too, so change `private_key_version` with it.",
				Validators: []validator.String{stringvalidator.RegexMatches(pemCert, "must be a PEM certificate (-----BEGIN CERTIFICATE-----)")}},
			"chain": schema.StringAttribute{Optional: true, Description: "Intermediate certificates, PEM-encoded.",
				Validators: []validator.String{stringvalidator.RegexMatches(pemCert, "must be PEM certificates (-----BEGIN CERTIFICATE-----)")}},
			"private_key": schema.StringAttribute{Required: true, Sensitive: true, WriteOnly: true,
				Description: "The private key, PEM-encoded. Write-only (Terraform 1.11 or newer): never stored. To send a new key, change `private_key_version`.",
				Validators:  []validator.String{stringvalidator.RegexMatches(pemKey, "must be a PEM private key (-----BEGIN ... PRIVATE KEY-----)")}},
			"private_key_version": schema.Int64Attribute{Optional: true,
				Description: "Any number; changing it sends `private_key` again. Write-only values can't be compared, so this is how a new key is noticed."},
			"common_name": schema.StringAttribute{Computed: true, Description: "Common name (CN), as MTN Cloud reads it from the certificate."},
			"self_signed": schema.BoolAttribute{Computed: true, Description: "Whether the certificate is self-signed."},
		},
	}
}

func (r *certificate) input(m certModel, key string) client.CertificateInput {
	return client.CertificateInput{Name: m.Name.ValueString(), Description: m.Description.ValueString(), DomainName: m.DomainName.ValueString(),
		Cert: m.Certificate.ValueString(), Chain: m.Chain.ValueString(), Key: key, Wildcard: m.Wildcard.ValueBool()}
}

// refresh copies what MTN Cloud reports into the model. The certificate, chain and key are never
// returned, so they stay as they are.
func refresh(m *certModel, c *client.Certificate) {
	m.ID, m.Name, m.Wildcard = types.StringValue(strconv.FormatInt(c.ID, 10)), types.StringValue(c.Name), types.BoolValue(c.Wildcard)
	m.Description, m.DomainName = emptyNull(c.Description), emptyNull(c.DomainName)
	m.CommonName, m.SelfSigned = types.StringValue(c.CommonName), types.BoolValue(c.SelfSigned)
	m.PrivateKey = types.StringNull()
}

func emptyNull(s string) types.String {
	if s == "" {
		return types.StringNull()
	}
	return types.StringValue(s)
}

func (r *certificate) read(ctx context.Context, id string, diags *diag.Diagnostics) *client.Certificate {
	n, err := strconv.ParseInt(id, 10, 64)
	if err != nil {
		diags.AddError("Invalid certificate ID", fmt.Sprintf("%q is not a certificate ID.", id))
		return nil
	}
	c, err := r.client.GetCertificate(ctx, n)
	if err != nil {
		if !client.IsNotFound(err) {
			diags.AddError("Unable to read the certificate", err.Error())
		}
		return nil
	}
	if c.TypeCode != "" && c.TypeCode != "internal" {
		diags.AddError("Not an uploaded certificate",
			fmt.Sprintf("Certificate %d is of type %q. Only certificates you upload (internal) can be managed; read others with the mtncloud_ssl_certificate data source.", n, c.TypeCode))
		return nil
	}
	return c
}

// ModifyPlan keeps common_name and self_signed from state unless the certificate itself changes, so
// a rename doesn't show them as "known after apply".
func (r *certificate) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.State.Raw.IsNull() || req.Plan.Raw.IsNull() {
		return
	}
	var before, after types.String
	resp.Diagnostics.Append(req.State.GetAttribute(ctx, path.Root("certificate"), &before)...)
	resp.Diagnostics.Append(req.Plan.GetAttribute(ctx, path.Root("certificate"), &after)...)
	if !before.Equal(after) {
		return
	}
	var cn types.String
	var self types.Bool
	resp.Diagnostics.Append(req.State.GetAttribute(ctx, path.Root("common_name"), &cn)...)
	resp.Diagnostics.Append(req.State.GetAttribute(ctx, path.Root("self_signed"), &self)...)
	resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("common_name"), cn)...)
	resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("self_signed"), self)...)
}

func (r *certificate) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	if !r.ready(&resp.Diagnostics) {
		return
	}
	var m certModel
	var key types.String
	resp.Diagnostics.Append(req.Plan.Get(ctx, &m)...)
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("private_key"), &key)...)
	if resp.Diagnostics.HasError() {
		return
	}
	// Names aren't unique in MTN Cloud: one already there is imported, never uploaded twice.
	same, err := r.client.FindCertificatesNamed(ctx, m.Name.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Unable to check for an existing certificate", err.Error())
		return
	}
	if len(same) > 0 {
		resp.Diagnostics.AddError("Certificate already exists",
			fmt.Sprintf("A certificate named %q already exists (ID %d). Import it (terraform import ... %d) or choose another name.", m.Name.ValueString(), same[0].ID, same[0].ID))
		return
	}
	id, err := r.client.CreateCertificate(ctx, r.input(m, key.ValueString()))
	if err != nil {
		resp.Diagnostics.AddError("Unable to create the certificate", err.Error())
		return
	}
	// Saved before the read: a failed read never leaves the certificate unmanaged.
	m.ID, m.PrivateKey = types.StringValue(strconv.FormatInt(id, 10)), types.StringNull()
	m.CommonName, m.SelfSigned = types.StringNull(), types.BoolNull()
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
	wctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	c, err := r.client.WaitForCertificate(wctx, id)
	if err != nil {
		resp.Diagnostics.AddError("Unable to read the new certificate", fmt.Sprintf("Certificate %d was created, but reading it failed: %s. The next apply reads it again.", id, err))
		return
	}
	desc, domain := m.Description, m.DomainName
	refresh(&m, c)
	m.Description, m.DomainName = keepEmpty(desc, m.Description), keepEmpty(domain, m.DomainName)
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

// keepEmpty keeps a configured "" where MTN Cloud reports nothing, so `description = ""` has no diff.
func keepEmpty(configured, read types.String) types.String {
	if read.IsNull() && configured.ValueString() == "" && !configured.IsNull() {
		return configured
	}
	return read
}

func (r *certificate) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	if !r.ready(&resp.Diagnostics) {
		return
	}
	var m certModel
	resp.Diagnostics.Append(req.State.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	c := r.read(ctx, m.ID.ValueString(), &resp.Diagnostics)
	if c == nil {
		if !resp.Diagnostics.HasError() {
			resp.State.RemoveResource(ctx)
		}
		return
	}
	desc, domain := m.Description, m.DomainName
	refresh(&m, c)
	m.Description, m.DomainName = keepEmpty(desc, m.Description), keepEmpty(domain, m.DomainName)
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

func (r *certificate) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	if !r.ready(&resp.Diagnostics) {
		return
	}
	var plan, state certModel
	var key types.String
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("private_key"), &key)...)
	if resp.Diagnostics.HasError() {
		return
	}
	plan.ID = state.ID
	id, err := strconv.ParseInt(state.ID.ValueString(), 10, 64)
	if err != nil {
		resp.Diagnostics.AddError("Invalid certificate ID", fmt.Sprintf("%q is not a certificate ID.", state.ID.ValueString()))
		return
	}
	in := r.input(plan, "")
	if !plan.PrivateKeyVersion.Equal(state.PrivateKeyVersion) {
		in.Key = key.ValueString()
	}
	in.ClearChain = plan.Chain.IsNull() && !state.Chain.IsNull()
	adopting := state.Certificate.IsNull() // after an import: the certificate and chain on file are unknown
	if adopting {
		in.Cert, in.Chain, in.ClearChain = "", "", false
	}
	same := plan.Name.Equal(state.Name) && plan.Description.ValueString() == state.Description.ValueString() &&
		plan.DomainName.ValueString() == state.DomainName.ValueString() && plan.Wildcard.Equal(state.Wildcard)
	// An adopted certificate is sent only what differs from what MTN Cloud has.
	if !adopting || !same || in.Key != "" {
		if err := r.client.UpdateCertificate(ctx, id, in); err != nil {
			resp.Diagnostics.AddError("Unable to update the certificate", err.Error())
			return
		}
	}
	c := r.read(ctx, plan.ID.ValueString(), &resp.Diagnostics)
	if c == nil {
		if !resp.Diagnostics.HasError() {
			resp.Diagnostics.AddError("Certificate not found after update", "It was deleted while it was being updated.")
		}
		return
	}
	desc, domain := plan.Description, plan.DomainName
	refresh(&plan, c)
	plan.Description, plan.DomainName = keepEmpty(desc, plan.Description), keepEmpty(domain, plan.DomainName)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *certificate) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	if !r.ready(&resp.Diagnostics) {
		return
	}
	var m certModel
	resp.Diagnostics.Append(req.State.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	id, err := strconv.ParseInt(m.ID.ValueString(), 10, 64)
	if err != nil {
		resp.Diagnostics.AddError("Invalid certificate ID", fmt.Sprintf("%q is not a certificate ID; nothing was deleted.", m.ID.ValueString()))
		return
	}
	if err := r.client.DeleteCertificate(ctx, id); err != nil {
		resp.Diagnostics.AddError("Unable to delete the certificate", err.Error())
	}
}

func (r *certificate) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	if _, err := strconv.ParseInt(req.ID, 10, 64); err != nil {
		resp.Diagnostics.AddError("Invalid import ID", fmt.Sprintf("Import a certificate by its numeric ID. Got %q.", req.ID))
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
}
