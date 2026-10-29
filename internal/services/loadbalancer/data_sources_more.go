package loadbalancer

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework-validators/datasourcevalidator"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"

	"github.com/mtn/terraform-provider-mtncloud/internal/client"
)

// --- L7 policies

type l7RuleDS struct {
	Type        string `tfsdk:"type"`
	CompareType string `tfsdk:"compare_type"`
	Key         string `tfsdk:"key"`
	Value       string `tfsdk:"value"`
	Invert      bool   `tfsdk:"invert"`
}

type l7PolicyModel struct {
	ID                 string     `tfsdk:"id"`
	Name               string     `tfsdk:"name"`
	Description        string     `tfsdk:"description"`
	Action             string     `tfsdk:"action"`
	Position           int64      `tfsdk:"position"`
	RedirectPoolID     string     `tfsdk:"redirect_pool_id"`
	RedirectURL        string     `tfsdk:"redirect_url"`
	RedirectPrefix     string     `tfsdk:"redirect_prefix"`
	RedirectHTTPCode   int64      `tfsdk:"redirect_http_code"`
	Rules              []l7RuleDS `tfsdk:"rules"`
	Enabled            bool       `tfsdk:"enabled"`
	ProvisioningStatus string     `tfsdk:"provisioning_status"`
	OperatingStatus    string     `tfsdk:"operating_status"`
}

func l7PolicyAttributes() map[string]schema.Attribute {
	return merge(statusAttributes(), map[string]schema.Attribute{
		"id":                 str("L7 policy ID."),
		"name":               str("Policy name, or empty."),
		"description":        str("Description, or empty."),
		"action":             str("`REDIRECT_TO_POOL`, `REDIRECT_TO_URL`, `REDIRECT_PREFIX` or `REJECT`."),
		"position":           num("Its place in the listener's order, from 1."),
		"redirect_pool_id":   str("For `REDIRECT_TO_POOL`: the pool's ID, or empty."),
		"redirect_url":       str("For `REDIRECT_TO_URL`: where it redirects, or empty."),
		"redirect_prefix":    str("For `REDIRECT_PREFIX`: the start of the new address, or empty."),
		"redirect_http_code": num("For redirects: the status code, or 0."),
		"rules": nested("The conditions a request must all meet, sorted by type and value.", map[string]schema.Attribute{
			"type":         str("What is compared, for example `PATH` or `HOST_NAME`."),
			"compare_type": str("`EQUAL_TO`, `STARTS_WITH`, `ENDS_WITH`, `CONTAINS` or `REGEX`."),
			"key":          str("The header or cookie name for `HEADER` and `COOKIE` rules, or empty."),
			"value":        str("The value compared with."),
			"invert":       flag("Whether it matches when the comparison fails."),
		}),
	})
}

func flattenL7Policies(in []client.LBL7Policy) []l7PolicyModel {
	out := make([]l7PolicyModel, 0, len(in))
	for _, p := range in {
		m := l7PolicyModel{
			ID: p.ID, Name: p.Name, Description: p.Description, Action: p.Action, Position: p.Position,
			RedirectPoolID: p.RedirectPoolID, RedirectURL: p.RedirectURL, RedirectPrefix: p.RedirectPrefix,
			RedirectHTTPCode: p.RedirectHTTPCode, Rules: []l7RuleDS{},
			Enabled: p.AdminStateUp, ProvisioningStatus: p.ProvisioningStatus, OperatingStatus: p.OperatingStatus,
		}
		for _, r := range p.Rules {
			m.Rules = append(m.Rules, l7RuleDS{Type: r.Type, CompareType: r.CompareType, Key: r.Key, Value: r.Value, Invert: r.Invert})
		}
		sortBy(m.Rules, func(r l7RuleDS) (string, string) { return r.Type, r.Key + "\x00" + r.Value + "\x00" + r.CompareType })
		out = append(out, m)
	}
	// In the listener's order: the order they are tried in.
	sortBy(out, func(m l7PolicyModel) (string, string) { return "", m.ID })
	stableByPosition(out)
	return out
}

func stableByPosition(ps []l7PolicyModel) {
	for i := 1; i < len(ps); i++ {
		for j := i; j > 0 && ps[j].Position < ps[j-1].Position; j-- {
			ps[j], ps[j-1] = ps[j-1], ps[j]
		}
	}
}

// listenerPolicies is a listener's policies, or an error when they could not be read: unread is
// not "none".
func listenerPolicies(l client.LBListener, diags *diag.Diagnostics) ([]l7PolicyModel, bool) {
	if l.L7Error != "" {
		diags.AddError("Unable to read the L7 policies", "The listener's L7 policies could not be read: "+l.L7Error+". Run the command again.")
		return nil, false
	}
	for _, p := range l.L7Policies {
		if p.RulesError != "" {
			diags.AddError("Unable to read the L7 policies", "The rules of L7 policy "+p.ID+" could not be read: "+p.RulesError+". Run the command again.")
			return nil, false
		}
	}
	return flattenL7Policies(l.L7Policies), true
}

func pickListener(lb *client.LoadBalancer, ctx context.Context, cfg tfsdk.Config, diags *diag.Diagnostics) (client.LBListener, bool) {
	id, name := pickInput(ctx, cfg, "listener", diags)
	return pick(lb.Listeners, func(l client.LBListener) string { return l.ID }, func(l client.LBListener) string { return l.Name }, id, name, "listener", "listener", diags)
}

type l7PoliciesState struct {
	partOf
	ListenerID   string          `tfsdk:"listener_id"`
	ListenerName string          `tfsdk:"listener_name"`
	Policies     []l7PolicyModel `tfsdk:"l7_policies"`
}

func newL7Policies() datasource.DataSource {
	return &dataSource{spec: spec{
		typeName: "load_balancer_l7_policies",
		about: "Lists the L7 policies of one listener, in the order they are tried, each with its rules. Pick the load balancer and " +
			"the listener by ID or name.",
		attributes: func() map[string]schema.Attribute {
			return merge(lbSelectorAttributes(), pickAttributes("listener", "listener"), map[string]schema.Attribute{
				"l7_policies": nested("The listener's L7 policies, by position.", l7PolicyAttributes()),
			})
		},
		validators: pickValidators("listener"),
		read: func(ctx context.Context, b *base, cfg tfsdk.Config, diags *diag.Diagnostics) any {
			lb, net := readPart(ctx, b, cfg, diags)
			if lb == nil {
				return nil
			}
			l, ok := pickListener(lb, ctx, cfg, diags)
			if !ok {
				return nil
			}
			ps, ok := listenerPolicies(l, diags)
			if !ok {
				return nil
			}
			return l7PoliciesState{partOfLB(net, lb), l.ID, l.Name, ps}
		},
	}}
}

type l7PolicyState struct {
	partOf
	ListenerID   string `tfsdk:"listener_id"`
	ListenerName string `tfsdk:"listener_name"`
	l7PolicyModel
}

func newL7Policy() datasource.DataSource {
	return &dataSource{spec: spec{
		typeName: "load_balancer_l7_policy",
		about: "Looks up one L7 policy of a listener by ID or by name, with its rules. Pick the load balancer and the listener by " +
			"ID or name.",
		attributes: func() map[string]schema.Attribute {
			return merge(lbSelectorAttributes(), pickAttributes("listener", "listener"), l7PolicyAttributes(), pickAttributes("", "L7 policy"))
		},
		validators: func() []datasource.ConfigValidator {
			return append(pickValidators("listener")(), datasourcevalidator.ExactlyOneOf(path.MatchRoot("id"), path.MatchRoot("name")))
		},
		read: func(ctx context.Context, b *base, cfg tfsdk.Config, diags *diag.Diagnostics) any {
			id, name := pickInput(ctx, cfg, "", diags)
			lb, net := readPart(ctx, b, cfg, diags)
			if lb == nil {
				return nil
			}
			l, ok := pickListener(lb, ctx, cfg, diags)
			if !ok {
				return nil
			}
			ps, ok := listenerPolicies(l, diags)
			if !ok {
				return nil
			}
			p, ok := pick(ps, func(p l7PolicyModel) string { return p.ID }, func(p l7PolicyModel) string { return p.Name }, id, name, "L7 policy", "", diags)
			if !ok {
				return nil
			}
			return l7PolicyState{partOfLB(net, lb), l.ID, l.Name, p}
		},
	}}
}

// --- one member

type memberState struct {
	partOf
	PoolID   string `tfsdk:"pool_id"`
	PoolName string `tfsdk:"pool_name"`
	memberModel
}

func newMember() datasource.DataSource {
	return &dataSource{spec: spec{
		typeName: "load_balancer_member",
		about: "Looks up one member of a pool by ID or by name. Pick the load balancer and the pool by ID or name. A member's name " +
			"is often empty; look it up by ID then, or list the pool's members.",
		attributes: func() map[string]schema.Attribute {
			return merge(lbSelectorAttributes(), pickAttributes("pool", "pool"), memberAttributes(), pickAttributes("", "member"))
		},
		validators: func() []datasource.ConfigValidator {
			return append(pickValidators("pool")(), datasourcevalidator.ExactlyOneOf(path.MatchRoot("id"), path.MatchRoot("name")))
		},
		read: func(ctx context.Context, b *base, cfg tfsdk.Config, diags *diag.Diagnostics) any {
			id, name := pickInput(ctx, cfg, "", diags)
			pid, pname := pickInput(ctx, cfg, "pool", diags)
			lb, net := readPart(ctx, b, cfg, diags)
			if lb == nil {
				return nil
			}
			p, ok := pickPool(lb, pid, pname, "pool", diags)
			if !ok {
				return nil
			}
			m, ok := pick(flattenMembers(p.Members), func(m memberModel) string { return m.ID }, func(m memberModel) string { return m.Name }, id, name, "member", "", diags)
			if !ok {
				return nil
			}
			return memberState{partOfLB(net, lb), p.ID, p.Name, m}
		},
	}}
}
