package loadbalancer

import (
	"regexp"
	"strings"
)

// The plugin's own messages (LbTopology.NAMED_GROUP_PROBLEM, BACKREF_PROBLEM), so a refusal reads
// the same at plan time as from the console.
const (
	regexNamedGroupProblem = "regex uses a named group (?P<name>...), which the load balancer cannot apply - use a plain group (...)"
	regexBackrefProblem    = `regex uses a back-reference (\1 or (?P=name)), which the load balancer cannot apply`
	regexLookbehindProblem = "regex uses a lookbehind (?<=...) or (?<!...), which the load balancer cannot apply"
)

var (
	regexVerbose = regexp.MustCompile(`\(\?[aiLmsux-]*x[aiLmsux-]*[:)]`)
	regexOctal   = regexp.MustCompile(`^[0-3][0-7][0-7]$`)
)

// regexKnownEscapes are the letter escapes the cloud's regex engine (Python's re) knows.
const regexKnownEscapes = "abBdDfnNrsStuUvwWxZA"

// l7RegexProblem says why the load balancer refuses a REGEX rule's value, or "" when it doesn't.
// It ports the plugin's LbTopology.regexProblem (2026-11-03): what Python's re refuses,
// plus named groups and back-references, which Python accepts but which leave the rule in ERROR and
// block every later change to the load balancer until the rule is removed.
func l7RegexProblem(rx string) string {
	// Before the verbose bypass: these break the load balancer whatever the mode.
	if strings.Contains(rx, "(?P<") {
		return regexNamedGroupProblem
	}
	if strings.Contains(rx, "(?P=") || regexHasBackref(rx) {
		return regexBackrefProblem
	}
	// Not in the plugin's check (yet): a lookbehind passes it, but the rule goes to ERROR (live, t139,
	// 2026-11-03: ^/x(?<=[a-z])$ on a clean load balancer).
	if strings.Contains(rx, "(?<=") || strings.Contains(rx, "(?<!") {
		return regexLookbehindProblem
	}
	// Verbose mode makes # start a comment; the plugin leaves such a pattern to the cloud to judge.
	if regexVerbose.MatchString(rx) {
		return ""
	}
	depth, inClass, classStart := 0, false, -1
	for i := 0; i < len(rx); i++ {
		c := rx[i]
		if c == '\\' {
			if i+1 >= len(rx) {
				return "regex ends with a lone backslash"
			}
			n := rx[i+1]
			letter := (n >= 'a' && n <= 'z') || (n >= 'A' && n <= 'Z')
			if letter && !strings.ContainsRune(regexKnownEscapes, rune(n)) {
				return `regex uses \` + string(n) + ", which the load balancer does not support"
			}
			if n == 'x' && (len(rx) < i+4 || !isHex(rx[i+2]) || !isHex(rx[i+3])) {
				return `regex uses \x without two hex digits (write \x41, not \x{41}), which the load balancer does not support`
			}
			i++
			continue
		}
		if inClass {
			if classEnds(rx, i, classStart) {
				inClass = false
			}
			continue
		}
		switch c {
		case '[':
			inClass, classStart = true, i
		case '(':
			// A (?#...) comment ends at its first ): a ( inside it opens nothing.
			if strings.HasPrefix(rx[i:], "(?#") {
				j := i + 3
				for j < len(rx) && rx[j] != ')' {
					if rx[j] == '\\' {
						j += 2
					} else {
						j++
					}
				}
				if j >= len(rx) {
					return "regex is not valid: a (?# comment is never closed"
				}
				i = j
				continue
			}
			if strings.HasPrefix(rx[i:], "(?<") && (i+3 >= len(rx) || (rx[i+3] != '=' && rx[i+3] != '!')) {
				return "regex uses (?< outside a lookbehind, which the load balancer does not support - use a plain group (...)"
			}
			depth++
		case ')':
			if depth--; depth < 0 {
				return "regex is not valid: a ) has no matching ("
			}
		}
	}
	if inClass {
		return "regex is not valid: a [ is never closed"
	}
	if depth > 0 {
		return "regex is not valid: a ( is never closed"
	}
	return ""
}

// regexHasBackref finds a numbered back-reference outside [ ]. Python reads \0, a three-digit
// octal escape like \101, and any digit escape inside [ ] as a character instead.
func regexHasBackref(rx string) bool {
	inClass, classStart := false, -1
	for i := 0; i < len(rx); i++ {
		c := rx[i]
		if c == '\\' {
			if !inClass && i+1 < len(rx) {
				rest := rx[i+1 : min(i+4, len(rx))]
				if rest[0] >= '1' && rest[0] <= '9' && !regexOctal.MatchString(rest) {
					return true
				}
			}
			i++
			continue
		}
		if inClass {
			if classEnds(rx, i, classStart) {
				inClass = false
			}
			continue
		}
		if c == '[' {
			inClass, classStart = true, i
		}
	}
	return false
}

// classEnds says whether the ] at i closes the class opened at start: a ] right after [ or [^ is a
// literal one.
func classEnds(rx string, i, start int) bool {
	return rx[i] == ']' && i > start+1 && (i != start+2 || rx[start+1] != '^')
}

func isHex(b byte) bool {
	return (b >= '0' && b <= '9') || (b >= 'a' && b <= 'f') || (b >= 'A' && b <= 'F')
}
