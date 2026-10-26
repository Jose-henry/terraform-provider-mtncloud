package client

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/mtn/terraform-provider-mtncloud/internal/normalize"
)

// ProjectByName finds a project by the name the customer gave it ("web-prod") or its full platform
// name ("web-prod-Acme"). The error lists the valid names, so nobody has to open the console.
func (c *Client) ProjectByName(ctx context.Context, name string) (Option, error) {
	who, err := c.WhoAmI(ctx)
	if err != nil {
		return Option{}, err
	}
	projects, err := c.Projects(ctx)
	if err != nil {
		return Option{}, err
	}
	var found []Option
	for _, p := range projects {
		if normalize.ProjectMatches(name, p.Name, who.AccountName) {
			found = append(found, p)
		}
	}
	switch len(found) {
	case 1:
		return found[0], nil
	case 0:
		names := make([]string, len(projects))
		for i, p := range projects {
			names[i] = normalize.ProjectShortName(p.Name, who.AccountName)
		}
		return Option{}, fmt.Errorf("no project named %q. Your projects: %s", name, list(names))
	}
	full := make([]string, len(found))
	for i, p := range found {
		full[i] = p.Name
	}
	return Option{}, fmt.Errorf("project name %q is ambiguous, it matches %s: use the full name", name, list(full))
}

// ImageByCode finds an instance type by its code, for example "MTN-U24.04LTS".
func (c *Client) ImageByCode(ctx context.Context, code string) (InstanceType, error) {
	types, err := c.InstanceTypes(ctx)
	if err != nil {
		return InstanceType{}, err
	}
	var images []string
	for _, t := range types {
		if t.Code == code {
			if t.LayoutID == 0 {
				return InstanceType{}, fmt.Errorf("image %q has no OpenStack layout, so it can't be provisioned on MTN Cloud", code)
			}
			return t, nil
		}
		if t.Category == "os" {
			images = append(images, t.Code)
		}
	}
	return InstanceType{}, fmt.Errorf("no image with code %q. Available images: %s", code, list(images))
}

func list(names []string) string {
	if len(names) == 0 {
		return "(none)"
	}
	s := slices.Clone(names)
	slices.Sort(s)
	return strings.Join(slices.Compact(s), ", ")
}
