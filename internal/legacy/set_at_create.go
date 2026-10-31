package legacy

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
)

// setAtCreate refuses, at plan time, a change to attributes MTN Cloud can't change once the object
// exists (owner, 2026-10-31: nothing is deleted and made again to fake a change). It replaces
// ForceNew; `terraform apply -replace` still makes a new object, as it leaves the values unchanged.
// A value known only at apply is checked when Terraform plans again then.
func setAtCreate(kind string, attrs ...string) schema.CustomizeDiffFunc {
	return func(_ context.Context, d *schema.ResourceDiff, _ interface{}) error {
		if d.Id() == "" {
			return nil // a create
		}
		for _, a := range attrs {
			if !d.HasChange(a) || !d.NewValueKnown(a) {
				continue
			}
			old, _ := d.GetChange(a)
			return fmt.Errorf("%s can't change: MTN Cloud can't change it once the %s exists, and Terraform won't delete it "+
				"to make a new one. Set it back to %s. To make a new %s, remove this one from the configuration (which deletes "+
				"it), then add the new one", a, kind, shown(old), kind)
		}
		return nil
	}
}

// shown is a value as the user wrote it, for an error; a secret is never shown.
func shown(v interface{}) string {
	if s, ok := v.(string); ok {
		return fmt.Sprintf("%q", s)
	}
	return fmt.Sprint(v)
}

// setAtCreateSecret is setAtCreate for sensitive attributes: the old value is never put in the error.
func setAtCreateSecret(kind string, attrs ...string) schema.CustomizeDiffFunc {
	return func(_ context.Context, d *schema.ResourceDiff, _ interface{}) error {
		if d.Id() == "" {
			return nil
		}
		for _, a := range attrs {
			if d.HasChange(a) && d.NewValueKnown(a) {
				fix := fmt.Sprintf("Set it back to the value the %s was made with", kind)
				if old, _ := d.GetChange(a); old == nil || old == "" {
					// Never read back, so an import leaves it empty: it is only used when the object is made.
					fix = fmt.Sprintf("If the %s was imported, remove %s from the configuration: it is only used when the %s is "+
						"made, and MTN Cloud doesn't return it", kind, a, kind)
				}
				return fmt.Errorf("%s can't change: MTN Cloud can't change it once the %s exists, and Terraform won't delete "+
					"it to make a new one. %s. To make a new %s, remove this one from the configuration (which deletes it), then "+
					"add the new one", a, kind, fix, kind)
			}
		}
		return nil
	}
}
