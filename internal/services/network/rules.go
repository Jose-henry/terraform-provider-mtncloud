// Package network holds the MTN networking resources: security groups and their rules (MTN Network
// and MTN Router follow). See dev-docs/api-research.md for the captured API shapes.
package network

import (
	"cmp"
	"slices"
	"strconv"
	"strings"

	"github.com/mtn/terraform-provider-mtncloud/internal/client"
	"github.com/mtn/terraform-provider-mtncloud/internal/normalize"
)

// rule is one security group rule in Terraform's terms. Empty strings mean "not set".
type rule struct {
	Direction             string
	Protocol              string
	PortRange             string // "" = all ports
	CIDR                  string // remote address; "" with no group = any address
	SourceSecurityGroupID string
	Description           string
}

// key identifies a rule by what it allows. Two rules with the same key are the same rule in
// OpenStack (Neutron refuses duplicates), whatever their description.
func (r rule) key() string {
	return strings.Join([]string{r.Direction, r.Protocol, r.PortRange, r.CIDR, r.SourceSecurityGroupID}, "|")
}

// fromAPI converts an API rule. Where the remote address lives depends on who made the rule:
// rules created through the API keep it in "source" for both directions (verified live), while
// rules synced from OpenStack put an egress rule's address in "destination" with sourceType "all".
func fromAPI(r client.SecurityGroupRule) rule {
	out := rule{
		Direction: strings.ToLower(r.Direction),
		Protocol:  strings.ToLower(r.Protocol),
		PortRange: normalize.PortRange(deref(r.PortRange)),
	}
	switch r.SourceType {
	case "cidr":
		out.CIDR = deref(r.Source)
	case "group":
		// The API also fills "source" with a CIDR for group rules; it isn't the rule's remote.
		if r.SourceGroupID != nil {
			out.SourceSecurityGroupID = strconv.FormatInt(*r.SourceGroupID, 10)
		}
	case "all":
		if out.Direction == "egress" {
			out.CIDR = deref(r.Destination)
		}
	}
	if r.Name != nil {
		out.Description = *r.Name
	}
	return out
}

// toInput is the create request for a rule.
func (r rule) toInput() client.SecurityGroupRuleInput {
	in := client.SecurityGroupRuleInput{
		Name: r.Description, Direction: r.Direction, Protocol: r.Protocol,
		PortRange: r.PortRange, RemoteCIDR: r.CIDR,
	}
	if r.SourceSecurityGroupID != "" {
		in.RemoteGroupID, _ = strconv.ParseInt(r.SourceSecurityGroupID, 10, 64) // validated at plan time
	}
	return in
}

// isDefaultEgress reports one of the two rules OpenStack adds to every new group (allow all
// outbound traffic, IPv4 and IPv6). They reach MTN Cloud with the next sync, as type "custom".
// A rule made through the API is "customRule", so a user's own allow-all egress rule never matches.
func isDefaultEgress(r client.SecurityGroupRule) bool {
	f := fromAPI(r)
	return r.RuleType == "custom" && f.Direction == "egress" && f.Protocol == "any" && f.PortRange == "" &&
		f.SourceSecurityGroupID == "" && isAllAddresses(f.CIDR)
}

// isAllAddresses is the remote of a default egress rule.
func isAllAddresses(cidr string) bool { return cidr == "0.0.0.0/0" || cidr == "::/0" }

// isDefaultEgressShape reports whether a configured rule would duplicate a default egress rule.
func (r rule) isDefaultEgressShape() bool {
	return r.Direction == "egress" && r.Protocol == "any" && r.PortRange == "" &&
		r.SourceSecurityGroupID == "" && isAllAddresses(r.CIDR)
}

// managedRules are the rules Terraform compares with its configuration: everything but the
// defaults, which are listed in all_rules and otherwise left alone.
func managedRules(rules []client.SecurityGroupRule) []client.SecurityGroupRule {
	var out []client.SecurityGroupRule
	for _, r := range rules {
		if !isDefaultEgress(r) {
			out = append(out, r)
		}
	}
	return out
}

// plan works out the API calls that turn the managed rules into the desired ones. A rule whose
// description changed is deleted and recreated (OpenStack rules are immutable). A desired rule
// without a description matches an existing rule whatever its name.
func planRules(current []client.SecurityGroupRule, desired []rule) (deleteIDs []int64, create []rule) {
	want := map[string]rule{}
	for _, d := range desired {
		want[d.key()] = d
	}
	have := map[string]bool{}
	for _, c := range managedRules(current) {
		f := fromAPI(c)
		d, ok := want[f.key()]
		if !ok || have[f.key()] || (d.Description != "" && d.Description != f.Description) {
			deleteIDs = append(deleteIDs, c.ID)
			continue
		}
		have[f.key()] = true
	}
	for _, d := range desired {
		if !have[d.key()] {
			create = append(create, d)
		}
	}
	slices.Sort(deleteIDs)
	sortRules(create)
	return deleteIDs, create
}

// readRules is the managed rule set to store after a read. prior is what the configuration or the
// state had; a prior rule without a description keeps it empty when the API returns a name.
func readRules(current []client.SecurityGroupRule, prior []rule) []rule {
	noDesc := map[string]bool{}
	for _, p := range prior {
		if p.Description == "" {
			noDesc[p.key()] = true
		}
	}
	var out []rule
	for _, c := range managedRules(current) {
		f := fromAPI(c)
		if noDesc[f.key()] {
			f.Description = ""
		}
		out = append(out, f)
	}
	sortRules(out)
	return out
}

func sortRules(rs []rule) {
	slices.SortFunc(rs, func(a, b rule) int {
		return cmp.Or(cmp.Compare(a.key(), b.key()), cmp.Compare(a.Description, b.Description))
	})
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
