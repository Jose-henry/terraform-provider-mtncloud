package client

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/mtn/terraform-provider-mtncloud/internal/normalize"
)

// SyncHint ends every "not found" for what a new project brings with it. A project is usable once its
// order's workflow succeeds, but MTN Cloud takes up to an hour to sync it, its default security
// group, external networks and floating IP pools; nothing in the provider waits for that (D-020).
const SyncHint = "If the project was created in the last hour, it may not have synced in yet (the project, " +
	"its default security group, external networks and floating IP pools): wait up to an hour, then plan again"

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
	return matchProject(projects, name, who.AccountName)
}

// matchProject picks the project a customer means by name, with an error listing the valid ones.
func matchProject(projects []Option, name, tenant string) (Option, error) {
	var found []Option
	for _, p := range projects {
		if normalize.ProjectMatches(name, p.Name, tenant) {
			found = append(found, p)
		}
	}
	switch len(found) {
	case 1:
		return found[0], nil
	case 0:
		names := make([]string, len(projects))
		for i, p := range projects {
			names[i] = normalize.ProjectShortName(p.Name, tenant)
		}
		return Option{}, &NoMatchError{fmt.Sprintf("no project named %q. Your projects: %s. %s", name, list(names), SyncHint)}
	}
	full := make([]string, len(found))
	for i, p := range found {
		full[i] = p.Name
	}
	return Option{}, fmt.Errorf("project name %q is ambiguous, it matches %s: use the full name", name, list(full))
}

// ExternalNetworkByName finds a network an MTN Router can use as its gateway, for example
// "public-network-02". Case is ignored, as the console does.
func (c *Client) ExternalNetworkByName(ctx context.Context, name string) (Option, error) {
	nets, err := c.ExternalNetworks(ctx)
	if err != nil {
		return Option{}, err
	}
	names := make([]string, len(nets))
	for i, n := range nets {
		if strings.EqualFold(n.Name, name) {
			return n, nil
		}
		names[i] = n.Name
	}
	return Option{}, fmt.Errorf("no external network named %q. Available: %s. %s", name, list(names), SyncHint)
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
				return InstanceType{}, fmt.Errorf("image %q has no layout MTN Cloud can provision", code)
			}
			return t, nil
		}
		if t.Category == "os" {
			images = append(images, t.Code)
		}
	}
	return InstanceType{}, fmt.Errorf("no image with code %q. Available images: %s", code, list(images))
}

// ImageByName finds an OS image by its name ("MTN Ubuntu Server 24.04.3LTS", case ignored) or its
// code ("MTN-U24.04LTS"). Only images with an OpenStack layout can be provisioned.
func (c *Client) ImageByName(ctx context.Context, name string) (InstanceType, error) {
	types, err := c.InstanceTypes(ctx)
	if err != nil {
		return InstanceType{}, err
	}
	var images []string
	for _, t := range types {
		if t.Code == name || strings.EqualFold(t.Name, name) {
			if t.LayoutID == 0 {
				return InstanceType{}, &NoMatchError{fmt.Sprintf("image %q has no layout MTN Cloud can provision", name)}
			}
			return t, nil
		}
		if t.Category == "os" {
			images = append(images, t.Name)
		}
	}
	return InstanceType{}, &NoMatchError{fmt.Sprintf("no image named %q. Available images: %s", name, list(images))}
}

// PlanByName finds a size, for example "G2S4", among the ones a project offers for a layout.
func (c *Client) PlanByName(ctx context.Context, p *Placement, projectID, layoutID int64, name string) (Plan, error) {
	plans, err := c.PlansAt(ctx, p, projectID, layoutID)
	if err != nil {
		return Plan{}, err
	}
	names := make([]string, len(plans))
	for i, pl := range plans {
		if strings.EqualFold(pl.Name, name) {
			return pl, nil
		}
		names[i] = pl.Name
	}
	return Plan{}, fmt.Errorf("no plan named %q for this image in this project. Available plans: %s", name, list(names))
}

// ProjectByNameAt is ProjectByName for one placement's cloud.
func (c *Client) ProjectByNameAt(ctx context.Context, p *Placement, name string) (Option, error) {
	who, err := c.WhoAmI(ctx)
	if err != nil {
		return Option{}, err
	}
	projects, err := c.ProjectsAt(ctx, p)
	if err != nil {
		return Option{}, err
	}
	return matchProject(projects, name, who.AccountName)
}

// SecurityGroupByName finds a security group placed in the project, by name (case ignored).
func (c *Client) SecurityGroupByName(ctx context.Context, p *Placement, projectID int64, name string) (Option, error) {
	groups, err := c.SecurityGroupsAt(ctx, p, projectID)
	if err != nil {
		return Option{}, err
	}
	names := make([]string, len(groups))
	for i, g := range groups {
		if strings.EqualFold(g.Name, name) {
			return g, nil
		}
		names[i] = g.Name
	}
	return Option{}, fmt.Errorf("no security group named %q in this project (a group needs the project as a location). "+
		"Available: %s. %s", name, list(names), SyncHint)
}

// DefaultSecurityGroup is the project's synced OpenStack default group, named "default" or
// "<Tenant>-Default-SG" (dev-docs/api-research.md P3). ok is false while the project has none yet.
func (c *Client) DefaultSecurityGroup(ctx context.Context, p *Placement, projectID int64) (Option, bool, error) {
	groups, err := c.SecurityGroupsAt(ctx, p, projectID)
	if err != nil {
		return Option{}, false, err
	}
	for _, g := range groups {
		if IsDefaultSecurityGroup(g.Name) {
			return g, true, nil
		}
	}
	return Option{}, false, nil
}

// SecurityGroupIDs is what a new server gets: the project's default group first, which MTN's
// provisioning workflow always applies, then the named groups, each ID once. Naming the default too
// is allowed and changes nothing.
func (c *Client) SecurityGroupIDs(ctx context.Context, p *Placement, projectID int64, names []string) ([]int64, error) {
	var ids []int64
	if projectID != 0 { // 0: the project is not known yet (a plan-time name check)
		def, ok, err := c.DefaultSecurityGroup(ctx, p, projectID)
		if err != nil {
			return nil, err
		}
		if ok {
			ids = append(ids, def.ID)
		}
	}
	for _, name := range names {
		g, err := c.SecurityGroupByName(ctx, p, projectID, name)
		if err != nil {
			return nil, err
		}
		if !slices.Contains(ids, g.ID) {
			ids = append(ids, g.ID)
		}
	}
	return ids, nil
}

// IsDefaultSecurityGroup reports the name of a project's synced default group.
func IsDefaultSecurityGroup(name string) bool {
	n := strings.ToLower(name)
	return n == "default" || strings.HasSuffix(n, "-default-sg")
}

// DatastoreByName finds a project's datastore, for example "__DEFAULT__".
func (c *Client) DatastoreByName(ctx context.Context, p *Placement, projectID int64, name string) (Option, error) {
	stores, err := c.DatastoresAt(ctx, p, projectID)
	if err != nil {
		return Option{}, err
	}
	names := make([]string, len(stores))
	for i, s := range stores {
		if strings.EqualFold(s.Name, name) {
			return s, nil
		}
		names[i] = s.Name
	}
	return Option{}, fmt.Errorf("no datastore named %q in this project. Available: %s", name, list(names))
}

// EnvironmentByName finds an environment by its name ("Production") or code ("production"), case
// ignored.
func (c *Client) EnvironmentByName(ctx context.Context, name string) (NamedValue, error) {
	envs, err := c.Environments(ctx)
	if err != nil {
		return NamedValue{}, err
	}
	names := make([]string, len(envs))
	for i, e := range envs {
		if strings.EqualFold(e.Name, name) || strings.EqualFold(e.Value, name) {
			return e, nil
		}
		names[i] = e.Name
	}
	return NamedValue{}, fmt.Errorf("no environment named %q. Available: %s", name, list(names))
}

// NetworkDomainByName finds a network domain by name, case and a trailing dot ignored.
func (c *Client) NetworkDomainByName(ctx context.Context, name string) (NetworkDomain, error) {
	domains, err := c.NetworkDomains(ctx)
	if err != nil {
		return NetworkDomain{}, err
	}
	want := strings.TrimSuffix(name, ".")
	names := make([]string, len(domains))
	for i, d := range domains {
		if strings.EqualFold(strings.TrimSuffix(d.Name, "."), want) {
			return d, nil
		}
		names[i] = d.Name
	}
	return NetworkDomain{}, fmt.Errorf("no network domain named %q. Available: %s", name, list(names))
}

func list(names []string) string {
	if len(names) == 0 {
		return "(none)"
	}
	s := slices.Clone(names)
	slices.Sort(s)
	return strings.Join(slices.Compact(s), ", ")
}

// NoMatchError says a name matched nothing the caller can see: the user's input is wrong, not the API.
type NoMatchError struct{ msg string }

func (e *NoMatchError) Error() string { return e.msg }

// IsNoMatch reports whether err is a NoMatchError.
func IsNoMatch(err error) bool {
	var e *NoMatchError
	return errors.As(err, &e)
}
