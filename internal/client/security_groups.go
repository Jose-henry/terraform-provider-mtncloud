package client

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
)

// Security groups: POST/GET/PUT/DELETE /api/security-groups[/{id}] and /{id}/rules[/{ruleId}].
// Shapes were captured as a customer (sub-tenant) on MTN Cloud, 2026-10-26, including a live
// create: see dev-docs/api-research.md. A group is created straight into one project (its
// "location"), and OpenStack adds two egress rules to it that only appear after the next sync.

// SecurityGroup is a security group and every rule on it.
type SecurityGroup struct {
	ID          int64
	Name        string
	Description string
	ExternalID  string // Neutron UUID
	Locations   []SecurityGroupLocation
	Rules       []SecurityGroupRule
}

// SecurityGroupLocation is where the group exists in OpenStack: one project.
type SecurityGroupLocation struct {
	ID          int64
	ExternalID  string
	Status      string
	ProjectID   int64
	ProjectName string // full platform name, for example "web-prod-AcmeCorp"
}

// SecurityGroupRule is one rule as the API returns it. Optional fields are nil when absent.
type SecurityGroupRule struct {
	ID              int64
	Name            *string
	RuleType        string // "customRule" when made through the API or console, "custom" when synced
	Direction       string // "ingress" or "egress"
	Protocol        string // "tcp", "udp", "icmp" or "any"
	PortRange       *string
	SourceType      string // "cidr", "group" or "all"
	Source          *string
	SourceGroupID   *int64
	Destination     *string
	DestinationType string
	ExternalID      *string
}

// SecurityGroupRuleInput is the rule to create. RemoteCIDR and RemoteGroupID are mutually
// exclusive; neither means any address.
type SecurityGroupRuleInput struct {
	Name          string
	Direction     string
	Protocol      string
	PortRange     string // "" for all ports
	RemoteCIDR    string
	RemoteGroupID int64
}

type wireRef struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

type wireRule struct {
	ID              int64    `json:"id"`
	Name            *string  `json:"name"`
	RuleType        string   `json:"ruleType"`
	Direction       string   `json:"direction"`
	Protocol        string   `json:"protocol"`
	PortRange       *string  `json:"portRange"`
	SourceType      string   `json:"sourceType"`
	Source          *string  `json:"source"`
	SourceGroup     *wireRef `json:"sourceGroup"`
	Destination     *string  `json:"destination"`
	DestinationType string   `json:"destinationType"`
	ExternalID      *string  `json:"externalId"`
}

func (w wireRule) rule() SecurityGroupRule {
	r := SecurityGroupRule{
		ID: w.ID, Name: w.Name, RuleType: w.RuleType, Direction: w.Direction, Protocol: w.Protocol,
		PortRange: w.PortRange, SourceType: w.SourceType, Source: w.Source, Destination: w.Destination,
		DestinationType: w.DestinationType, ExternalID: w.ExternalID,
	}
	if w.SourceGroup != nil {
		id := w.SourceGroup.ID
		r.SourceGroupID = &id
	}
	return r
}

type wireSecurityGroup struct {
	ID          int64   `json:"id"`
	Name        string  `json:"name"`
	Description *string `json:"description"`
	ExternalID  *string `json:"externalId"`
	Locations   []struct {
		ID         int64    `json:"id"`
		ExternalID *string  `json:"externalId"`
		Status     string   `json:"status"`
		ZonePool   *wireRef `json:"zonePool"`
	} `json:"locations"`
	Rules []wireRule `json:"rules"`
}

func (w wireSecurityGroup) group() *SecurityGroup {
	g := &SecurityGroup{ID: w.ID, Name: w.Name, Description: deref(w.Description), ExternalID: deref(w.ExternalID)}
	for _, l := range w.Locations {
		loc := SecurityGroupLocation{ID: l.ID, ExternalID: deref(l.ExternalID), Status: l.Status}
		if l.ZonePool != nil {
			loc.ProjectID, loc.ProjectName = l.ZonePool.ID, l.ZonePool.Name
		}
		g.Locations = append(g.Locations, loc)
	}
	for _, r := range w.Rules {
		g.Rules = append(g.Rules, r.rule())
	}
	return g
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// CreateSecurityGroup creates a group directly in a project. OpenStack creates it in the same call.
func (c *Client) CreateSecurityGroup(ctx context.Context, name, description string, projectID int64) (*SecurityGroup, error) {
	p, err := c.Placement(ctx)
	if err != nil {
		return nil, err
	}
	body := map[string]any{"securityGroup": map[string]any{
		"name":          name,
		"description":   description,
		"zoneId":        p.CloudID,
		"customOptions": map[string]any{"resourcePoolId": projectID},
	}}
	var resp struct {
		SecurityGroup wireSecurityGroup `json:"securityGroup"`
	}
	if err := c.do(ctx, http.MethodPost, "/api/security-groups", nil, body, &resp); err != nil {
		return nil, err
	}
	return resp.SecurityGroup.group(), nil
}

// GetSecurityGroup reads a group and its rules. A missing group is an APIError with IsNotFound.
func (c *Client) GetSecurityGroup(ctx context.Context, id int64) (*SecurityGroup, error) {
	var resp struct {
		SecurityGroup wireSecurityGroup `json:"securityGroup"`
	}
	if err := c.get(ctx, "/api/security-groups/"+itoa(id), nil, &resp); err != nil {
		return nil, err
	}
	return resp.SecurityGroup.group(), nil
}

// FindSecurityGroup returns the group with exactly this name in the project, or nil. Used to make
// Create idempotent after a request whose outcome is unknown (a timeout or a dropped connection).
func (c *Client) FindSecurityGroup(ctx context.Context, name string, projectID int64) (*SecurityGroup, error) {
	items, err := listAll[wireSecurityGroup](ctx, c, "/api/security-groups", "securityGroups", url.Values{"name": {name}})
	if err != nil {
		return nil, err
	}
	for _, w := range items {
		if w.Name != name {
			continue
		}
		for _, l := range w.Locations {
			if l.ZonePool != nil && l.ZonePool.ID == projectID {
				return w.group(), nil
			}
		}
	}
	return nil, nil
}

// UpdateSecurityGroup changes the name and description.
func (c *Client) UpdateSecurityGroup(ctx context.Context, id int64, name, description string) error {
	body := map[string]any{"securityGroup": map[string]any{"name": name, "description": description}}
	return c.do(ctx, http.MethodPut, "/api/security-groups/"+itoa(id), nil, body, nil)
}

// DeleteSecurityGroup deletes a group. A group that is already gone counts as deleted.
func (c *Client) DeleteSecurityGroup(ctx context.Context, id int64) error {
	return ignoreNotFound(c.do(ctx, http.MethodDelete, "/api/security-groups/"+itoa(id), nil, nil, nil))
}

// CreateSecurityGroupRule adds a rule. The remote address is sent as the source for both
// directions, which is how the API stores rules it creates (verified live for egress).
func (c *Client) CreateSecurityGroupRule(ctx context.Context, groupID int64, in SecurityGroupRuleInput) (*SecurityGroupRule, error) {
	if in.RemoteCIDR != "" && in.RemoteGroupID != 0 {
		return nil, errors.New("a security group rule takes a CIDR or a source security group, not both")
	}
	rule := map[string]any{
		"ruleType":        "customRule",
		"direction":       in.Direction,
		"protocol":        in.Protocol,
		"destinationType": "instance",
	}
	if in.Name != "" {
		rule["name"] = in.Name
	}
	if in.PortRange != "" {
		rule["portRange"] = in.PortRange
	}
	switch {
	case in.RemoteGroupID != 0:
		rule["sourceType"] = "group"
		rule["sourceGroup"] = map[string]any{"id": in.RemoteGroupID}
	case in.RemoteCIDR != "":
		rule["sourceType"] = "cidr"
		rule["source"] = in.RemoteCIDR
	default:
		rule["sourceType"] = "all"
	}
	var resp struct {
		Rule wireRule `json:"rule"`
	}
	if err := c.do(ctx, http.MethodPost, fmt.Sprintf("/api/security-groups/%d/rules", groupID), nil, map[string]any{"rule": rule}, &resp); err != nil {
		return nil, err
	}
	r := resp.Rule.rule()
	return &r, nil
}

// DeleteSecurityGroupRule deletes a rule. A rule that is already gone counts as deleted.
func (c *Client) DeleteSecurityGroupRule(ctx context.Context, groupID, ruleID int64) error {
	return ignoreNotFound(c.do(ctx, http.MethodDelete, fmt.Sprintf("/api/security-groups/%d/rules/%d", groupID, ruleID), nil, nil, nil))
}

func ignoreNotFound(err error) error {
	if IsNotFound(err) {
		return nil
	}
	return err
}
