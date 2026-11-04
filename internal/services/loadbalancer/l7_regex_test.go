package loadbalancer

import (
	"context"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/diag"

	"github.com/mtn/terraform-provider-mtncloud/internal/client"
)

func TestL7RegexProblem(t *testing.T) {
	for _, c := range []struct{ rx, want string }{
		// Accepted, as by the plugin and Python's re.
		{`^/api/.*`, ""},
		{`^/(api|v[0-9]+)/(?:x|y)$`, ""},
		{`a<b`, ""},
		{`\0`, ""},
		{`\101`, ""},
		{`\377`, ""},
		{`[\1]`, ""},
		{`[^\2-\9]`, ""},
		{`[]\1]`, ""}, // ] first in a class is literal, so \1 is still inside it
		{`(?<=a)b`, regexLookbehindProblem},
		{`b(?<!c)`, regexLookbehindProblem},
		{`(?=a)b(?!c)`, ""},
		{`\d+\.\w\x41`, ""},
		{`(?#a ( comment)x`, ""},
		{`(?x) \1 # verbose: refused anyway`, regexBackrefProblem},
		{`(?x) [ # verbose: left to the cloud`, ""},
		// Named groups and back-references.
		{`(?P<x>a)`, regexNamedGroupProblem},
		{`(a)(?P=x)`, regexBackrefProblem},
		{`(a)\1`, regexBackrefProblem},
		{`(a)\9`, regexBackrefProblem},
		{`(a)\12`, regexBackrefProblem},
		{`(a)\18`, regexBackrefProblem},  // 8 isn't octal
		{`(a)\400`, regexBackrefProblem}, // past \377: Python reads it as group 40
		{`[a]\1`, regexBackrefProblem},
		{`[]]\1`, regexBackrefProblem},
		{`[^]]\1`, regexBackrefProblem},
		// What Python's re refuses.
		{`\p{L}`, `regex uses \p, which the load balancer does not support`},
		{`\x{41}`, `\x without two hex digits`},
		{`\x4`, `\x without two hex digits`},
		{`(?<x>a)`, `regex uses (?<, which the load balancer does not support`},
		{`(a`, `a ( is never closed`},
		{`a)`, `a ) has no matching (`},
		{`[a`, `a [ is never closed`},
		{`a\`, `lone backslash`},
		{`(?#open`, `comment is never closed`},
	} {
		got := l7RegexProblem(c.rx)
		if (c.want == "") != (got == "") || !strings.Contains(got, c.want) {
			t.Errorf("%q: got %q, want %q", c.rx, got, c.want)
		}
	}
}

func TestLBWarnings(t *testing.T) {
	var diags diag.Diagnostics
	ctx, done := lbWarnings(context.Background(), &diags)
	done()
	if len(diags) != 0 {
		t.Fatalf("no saves, but %v", diags)
	}
	diags = nil
	ctx, done = lbWarnings(ctx, &diags)
	if !client.AddLBWarning(ctx, "L7 rule 'old' is in ERROR.") {
		t.Fatal("no collector on the context")
	}
	done()
	if len(diags) != 1 || diags.WarningsCount() != 1 || diags[0].Summary() != "Load balancer warning" || diags[0].Detail() != "L7 rule 'old' is in ERROR." {
		t.Fatalf("diags %v", diags)
	}
}
