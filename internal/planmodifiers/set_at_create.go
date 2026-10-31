package planmodifiers

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
)

// SetAtCreate is for an attribute MTN Cloud can't change once the object exists (owner, 2026-10-31:
// what the API or console can't change isn't editable, and nothing is replaced to fake a change). A
// change is refused at plan time, so nothing is deleted; `terraform apply -replace` still makes a new
// one, as it leaves the value unchanged. It generalises the instance's create-only days and the
// project's "can't be changed" check.
//
// A value not yet known at plan is checked when Terraform plans again at apply. A reference to a
// parent object (network_id, instance_id, ...) keeps RequiresReplace instead: when the parent is
// replaced its attachments must follow, and nothing of theirs is lost.
func SetAtCreate(kind string) SetAtCreateModifier {
	return SetAtCreateModifier{kind: kind}
}

// SetAtCreateUnlessImported is SetAtCreate for an attribute an import leaves null: the first value
// set after an import is adopted, not refused.
func SetAtCreateUnlessImported(kind string) SetAtCreateModifier {
	return SetAtCreateModifier{kind: kind, adoptNull: true}
}

// SetAtCreateSensitive is SetAtCreate for a sensitive attribute: the error never shows its value.
func SetAtCreateSensitive(kind string) SetAtCreateModifier {
	return SetAtCreateModifier{kind: kind, sensitive: true}
}

// SetAtCreateModifier implements the plan modifier interfaces for every attribute type it is used on.
type SetAtCreateModifier struct {
	kind      string // what the object is called in the error, for example "router"
	adoptNull bool
	sensitive bool // never put the value in the error
}

var (
	_ planmodifier.String = SetAtCreateModifier{}
	_ planmodifier.Int64  = SetAtCreateModifier{}
	_ planmodifier.Bool   = SetAtCreateModifier{}
	_ planmodifier.List   = SetAtCreateModifier{}
	_ planmodifier.Set    = SetAtCreateModifier{}
	_ planmodifier.Map    = SetAtCreateModifier{}
)

func (m SetAtCreateModifier) Description(context.Context) string {
	return fmt.Sprintf("Set when the %s is created; MTN Cloud can't change it afterwards.", m.kind)
}

func (m SetAtCreateModifier) MarkdownDescription(ctx context.Context) string {
	return m.Description(ctx)
}

func (m SetAtCreateModifier) check(state tfsdk.State, at path.Path, from, to attr.Value, diags *diag.Diagnostics) {
	switch {
	case state.Raw.IsNull(), to.IsUnknown(), from.Equal(to):
		return // a create, a value known only at apply (checked then), or no change
	case from.IsNull() && m.adoptNull:
		return
	}
	if m.sensitive {
		refuse(diags, at, m.kind, "Set it back to the value the "+m.kind+" was made with.")
		return
	}
	RefuseChange(diags, at, m.kind, from)
}

// RefuseChange reports a change MTN Cloud can't make to an existing object, for a check made in
// ModifyPlan rather than by SetAtCreate. from is the value the object has.
func RefuseChange(diags *diag.Diagnostics, at path.Path, kind string, from attr.Value) {
	fix := fmt.Sprintf("Set it back to %s.", from.String())
	if from.IsNull() {
		fix = "Remove it: the " + kind + " was made without it."
	}
	refuse(diags, at, kind, fix)
}

func refuse(diags *diag.Diagnostics, at path.Path, kind, fix string) {
	diags.AddAttributeError(at, fmt.Sprintf("%s can't change", at),
		fmt.Sprintf("MTN Cloud can't change %s once the %s exists, and Terraform won't delete it to make a new one. %s "+
			"To make a new %s, remove this one from the configuration (which deletes it), then add the new one.",
			at, kind, fix, kind))
}

func (m SetAtCreateModifier) PlanModifyString(_ context.Context, req planmodifier.StringRequest, resp *planmodifier.StringResponse) {
	m.check(req.State, req.Path, req.StateValue, req.PlanValue, &resp.Diagnostics)
}

func (m SetAtCreateModifier) PlanModifyInt64(_ context.Context, req planmodifier.Int64Request, resp *planmodifier.Int64Response) {
	m.check(req.State, req.Path, req.StateValue, req.PlanValue, &resp.Diagnostics)
}

func (m SetAtCreateModifier) PlanModifyBool(_ context.Context, req planmodifier.BoolRequest, resp *planmodifier.BoolResponse) {
	m.check(req.State, req.Path, req.StateValue, req.PlanValue, &resp.Diagnostics)
}

func (m SetAtCreateModifier) PlanModifyList(_ context.Context, req planmodifier.ListRequest, resp *planmodifier.ListResponse) {
	m.check(req.State, req.Path, req.StateValue, req.PlanValue, &resp.Diagnostics)
}

func (m SetAtCreateModifier) PlanModifySet(_ context.Context, req planmodifier.SetRequest, resp *planmodifier.SetResponse) {
	m.check(req.State, req.Path, req.StateValue, req.PlanValue, &resp.Diagnostics)
}

func (m SetAtCreateModifier) PlanModifyMap(_ context.Context, req planmodifier.MapRequest, resp *planmodifier.MapResponse) {
	m.check(req.State, req.Path, req.StateValue, req.PlanValue, &resp.Diagnostics)
}
