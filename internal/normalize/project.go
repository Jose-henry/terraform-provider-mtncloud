// Package normalize holds pure functions that make API values and user config compare equal, so
// Terraform never shows a diff for a cosmetic difference.
package normalize

import "strings"

// ProjectShortName returns the name a customer gave a project, from the platform's full name.
//
// MTN's "Create Project" automation renames every project to "<name>-<TENANT>", where TENANT is
// the tenant (account) name from /api/whoami: "web-prod" in tenant "Acme" becomes
// "web-prod-Acme". Customers only ever see and type the short name. Some pools are shared and have
// no suffix at all (for example "admin"); those come back unchanged.
//
// TENANT is the tenant's account name from /api/whoami (user.account.name), never the login
// subdomain: user acme\jdoe belongs to tenant "AcmeCorp", whose pools are "testproject-AcmeCorp".
//
// Matching is case-sensitive on purpose (owner's decision): OpenStack names are case-sensitive,
// so "web-prod-acme" is not a project of tenant "Acme", and two projects may differ only by case.
//
// Examples (see project_test.go for the full table):
//
//	ProjectShortName("web-prod-Acme", "Acme") == "web-prod"
//	ProjectShortName("admin", "Acme")         == "admin"
func ProjectShortName(fullName, tenant string) string {
	if tenant == "" {
		return fullName
	}
	short, ok := strings.CutSuffix(fullName, "-"+tenant)
	if !ok || short == "" {
		return fullName
	}
	return short
}

// ProjectMatches reports whether what the user typed names this project. Both the short name and
// the full platform name are accepted, so configs written either way keep working.
func ProjectMatches(input, fullName, tenant string) bool {
	return input == fullName || input == ProjectShortName(fullName, tenant)
}
