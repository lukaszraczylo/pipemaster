package gitlab_test

// Rules pinned here come from the GitLab CI/CD job rules reference
// (https://docs.gitlab.com/ci/jobs/job_rules/) and the expression lexemes in
// lib/gitlab/ci/pipeline/expression (https://gitlab.com/gitlab-org/gitlab, master): strings have no
// escapes, `$VAR` is `\$\w+`, `${VAR}` has no lexeme, || binds loosest then && then the comparisons.

import (
	"reflect"
	"strings"
	"testing"

	"github.com/lukaszraczylo/pipemaster/expr"
	"github.com/lukaszraczylo/pipemaster/expr/gitlab"
	"pgregory.net/rapid"
)

func r(p ...string) expr.Expr                 { return expr.Ref{Path: p} }
func bin(op string, l, x expr.Expr) expr.Expr { return expr.Binary{Op: op, L: l, R: x} }
func str(s string) expr.Expr                  { return expr.Lit{Kind: expr.LitStr, Str: s} }
func not(x expr.Expr) expr.Expr               { return expr.Unary{Op: expr.OpNot, X: x} }

var null = expr.Lit{}

func TestParsePrintTable(t *testing.T) {
	cases := []struct {
		src  string
		want expr.Expr
		out  string // printed form when it differs from src
	}{
		{`$CI_COMMIT_BRANCH == "main"`, bin(expr.OpEq, r("branch"), str("main")), ""},
		{`$CI_COMMIT_BRANCH == 'main'`, bin(expr.OpEq, r("branch"), str("main")), `$CI_COMMIT_BRANCH == "main"`},
		{`$CI_COMMIT_TAG != null`, bin(expr.OpNe, r("tag"), null), ""},
		{`$CI_COMMIT_SHA`, r("sha"), ""},
		{`$CI_PIPELINE_SOURCE == "merge_request_event"`, bin(expr.OpEq, r("event"), str("merge_request_event")), ""},
		{`$CI_PROJECT_PATH == "a/b"`, bin(expr.OpEq, r("repo"), str("a/b")), ""},
		{`$GITLAB_USER_LOGIN == "bob"`, bin(expr.OpEq, r("actor"), str("bob")), ""},
		{`$CI_MERGE_REQUEST_IID`, r("pr", "number"), ""},
		{`$CI_COMMIT_REF_NAME`, r("ref_name"), ""},
		{`$CI_JOB_NAME == "x"`, bin(expr.OpEq, r("gitlab", "CI_JOB_NAME"), str("x")), ""},
		{`$A == $B`, bin(expr.OpEq, r("gitlab", "A"), r("gitlab", "B")), ""},
		{`$A == ""`, bin(expr.OpEq, r("gitlab", "A"), str("")), ""},
		{`$A =~ /^release-/`, bin(expr.OpMatch, r("gitlab", "A"), str("^release-")), ""},
		{`$A !~ /^release-/i`, bin(expr.OpNoMatch, r("gitlab", "A"), str("(?i)^release-")), ""},
		{`$A =~ /a\/b/`, bin(expr.OpMatch, r("gitlab", "A"), str("a/b")), ""},
		{`$A && $B || $C`, bin(expr.OpOr, bin(expr.OpAnd, r("gitlab", "A"), r("gitlab", "B")), r("gitlab", "C")), ""},
		{`$A || $B && $C`, bin(expr.OpOr, r("gitlab", "A"), bin(expr.OpAnd, r("gitlab", "B"), r("gitlab", "C"))), ""},
		{`($A || $B) && $C`, bin(expr.OpAnd, bin(expr.OpOr, r("gitlab", "A"), r("gitlab", "B")), r("gitlab", "C")), ""},
		{`$A == "x" && $B != "y"`, bin(expr.OpAnd, bin(expr.OpEq, r("gitlab", "A"), str("x")), bin(expr.OpNe, r("gitlab", "B"), str("y"))), ""},
		{`!$A`, not(r("gitlab", "A")), ""},
		{`!($A == "x")`, not(bin(expr.OpEq, r("gitlab", "A"), str("x"))), ""},
		{`!$A == "x"`, bin(expr.OpEq, not(r("gitlab", "A")), str("x")), ""},
		{`$A == 'say "hi"'`, bin(expr.OpEq, r("gitlab", "A"), str(`say "hi"`)), ""},
		{`$A == true`, bin(expr.OpEq, r("gitlab", "A"), expr.Lit{Kind: expr.LitBool, Bool: true}), ""},
		{`(($A))`, r("gitlab", "A"), `$A`},
	}
	for _, c := range cases {
		got, err := gitlab.Parse(c.src)
		if err != nil {
			t.Errorf("Parse(%q): %v", c.src, err)
			continue
		}
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("Parse(%q) = %#v, want %#v", c.src, got, c.want)
			continue
		}
		want := c.out
		if want == "" {
			want = c.src
		}
		out, err := gitlab.Print(got)
		if err != nil || out != want {
			t.Errorf("Print(Parse(%q)) = %q, %v; want %q", c.src, out, err, want)
		}
	}
}

func TestOpaqueForUnmodelledSyntax(t *testing.T) {
	for _, src := range []string{
		`$A =~ /xy/s`, `$A =~ /xy/m`, `$A =~ /xy/U`, `$A =~ $B`, `$A =~ "xy"`,
		`$[[ inputs.env ]] == "x"`,
	} {
		got, err := gitlab.Parse(src)
		if err != nil {
			t.Errorf("Parse(%q): %v", src, err)
			continue
		}
		if o, ok := got.(expr.Opaque); !ok || o.Dialect != "gitlab" || o.Text != src {
			t.Errorf("Parse(%q) = %#v, want gitlab Opaque", src, got)
		}
	}
}

func TestParseErrors(t *testing.T) {
	for src, why := range map[string]string{
		``:                       "empty",
		`$A ==`:                  "missing operand",
		`${A}`:                   "braced variable has no lexeme",
		`$`:                      "bare dollar",
		`$A == "x`:               "unterminated string",
		`$A =~ /x/`:              "single-character pattern",
		`$A =~ /xy\/`:            "unterminated pattern",
		`$A =~ /(/`:              "invalid RE2",
		`/xy/`:                   "pattern outside =~",
		`($A`:                    "unmatched paren",
		`$A)`:                    "stray paren",
		`$A & $B`:                "single ampersand",
		`foo`:                    "unknown word",
		`$A == "x" "y"`:          "trailing token",
		strings.Repeat("(", 60):  "nesting too deep",
		strings.Repeat("!", 100): "unary nesting too deep",
	} {
		if _, err := gitlab.Parse(src); err == nil {
			t.Errorf("Parse(%q) succeeded, want error (%s)", src, why)
		}
	}
}

func TestPrintErrors(t *testing.T) {
	for name, e := range map[string]expr.Expr{
		"number":         expr.Lit{Kind: expr.LitNum, Num: 1},
		"call":           expr.Call{Name: "startsWith"},
		"ordering":       bin(expr.OpLt, r("sha"), str("a")),
		"ref_type":       r("ref_type"),
		"gha native":     r("gha", "run_id"),
		"both quotes":    bin(expr.OpEq, r("sha"), str(`a"b'c`)),
		"one char regex": bin(expr.OpMatch, r("sha"), str("a")),
		"backslash end":  bin(expr.OpMatch, r("sha"), str(`ab\`)),
		"regex not lit":  bin(expr.OpMatch, r("sha"), r("sha")),
		"foreign opaque": expr.Opaque{Dialect: "gha", Text: "x"},
		"bad native":     r("gitlab", "a b"),
	} {
		if s, err := gitlab.Print(e); err == nil {
			t.Errorf("%s: Print = %q, want error", name, s)
		}
	}
}

func TestPrintParensAndSlash(t *testing.T) {
	e := bin(expr.OpAnd, bin(expr.OpOr, r("gitlab", "A"), r("gitlab", "B")), not(bin(expr.OpMatch, r("sha"), str("(?i)a/b"))))
	got, err := gitlab.Print(e)
	if want := `($A || $B) && !($CI_COMMIT_SHA =~ /a\/b/i)`; err != nil || got != want {
		t.Errorf("Print = %q, %v; want %q", got, err, want)
	}
}

type env map[string]expr.Value

func (e env) Lookup(p []string) (expr.Value, bool) { v, ok := e[strings.Join(p, ".")]; return v, ok }
func (env) Status() expr.Status                    { return expr.Success }

func truth(t *testing.T, src string, e env) bool {
	t.Helper()
	x, err := gitlab.Parse(src)
	if err != nil {
		t.Fatalf("Parse(%q): %v", src, err)
	}
	v, err := expr.Eval(x, e, gitlab.Semantics)
	if err != nil {
		t.Fatalf("Eval(%q): %v", src, err)
	}
	return gitlab.Semantics.Truthy(v)
}

func TestSemanticsPinned(t *testing.T) {
	undef, empty, set := env{}, env{"gitlab.A": ""}, env{"gitlab.A": "Main"}
	for _, c := range []struct {
		src                 string
		undef, empty, isSet bool
	}{
		{`$A`, false, false, true},
		{`$A == null`, true, false, false},
		{`$A != null`, false, true, true},
		{`$A == ""`, false, true, false},
		{`$A != ""`, true, false, true},
		{`$A == "main"`, false, false, false},
		{`$A == "Main"`, false, false, true},
		{`$A =~ /^main$/`, false, false, false},
		{`$A =~ /^main$/i`, false, false, true},
		{`$A !~ /^main$/i`, true, true, false},
		{`!$A`, true, true, false},
		// && and || test operands with Ruby truthiness: "" is truthy there, so the
		// empty variable does not short-circuit although a bare `$A` is falsy at the top.
		{`$A && "x"`, false, true, true},
		{`$A || "x"`, true, false, true},
		{`$A && $B`, false, false, false},
	} {
		for name, got := range map[string]bool{"undef": truth(t, c.src, undef), "empty": truth(t, c.src, empty), "set": truth(t, c.src, set)} {
			want := map[string]bool{"undef": c.undef, "empty": c.empty, "set": c.isSet}[name]
			if got != want {
				t.Errorf("%s with %s variable = %v, want %v", c.src, name, got, want)
			}
		}
	}
}

func TestAndBindsTighterThanOr(t *testing.T) {
	e := env{"gitlab.A": "1", "gitlab.B": "", "gitlab.C": ""}
	// A || (B && C) is true; (A || B) && C would be false.
	if !truth(t, `$A == "1" || $B == "x" && $C == "x"`, e) {
		t.Error("&& must bind tighter than ||")
	}
}

func TestEventMap(t *testing.T) {
	want := map[string]string{"push": "push", "pull_request": "merge_request_event", "schedule": "schedule", "workflow_dispatch": "web"}
	if !reflect.DeepEqual(gitlab.Events, want) {
		t.Errorf("Events = %v", gitlab.Events)
	}
}

func genStr(t *rapid.T, label string) string {
	return rapid.StringMatching(`[A-Za-z0-9_/ .'-]{0,6}`).Draw(t, label)
}

func genNode(t *rapid.T, depth int) expr.Expr {
	k := rapid.IntRange(0, 6).Draw(t, "k")
	if depth <= 0 {
		k %= 3
	}
	switch k {
	case 0:
		return expr.Ref{Path: rapid.SampledFrom([][]string{{"branch"}, {"tag"}, {"sha"}, {"event"}, {"ref_name"}, {"pr", "number"}, {"gitlab", "CI_JOB_NAME"}, {"gitlab", "x1"}}).Draw(t, "ref")}
	case 1:
		return expr.Lit{Kind: expr.LitStr, Str: genStr(t, "s")}
	case 2:
		return rapid.SampledFrom([]expr.Expr{null, expr.Lit{Kind: expr.LitBool, Bool: true}, expr.Lit{Kind: expr.LitBool}}).Draw(t, "lit")
	case 3:
		return expr.Unary{Op: expr.OpNot, X: genNode(t, depth-1)}
	case 4, 5:
		return expr.Binary{Op: rapid.SampledFrom([]string{expr.OpEq, expr.OpNe, expr.OpAnd, expr.OpOr}).Draw(t, "op"), L: genNode(t, depth-1), R: genNode(t, depth-1)}
	}
	pat := rapid.StringMatching(`(\(\?i\))?[a-z/^$.*]{2,6}`).Draw(t, "pat")
	return expr.Binary{Op: rapid.SampledFrom([]string{expr.OpMatch, expr.OpNoMatch}).Draw(t, "mop"), L: genNode(t, depth-1), R: expr.Lit{Kind: expr.LitStr, Str: pat}}
}

func TestPrintParseRoundTrip(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		e := genNode(t, 4)
		s, err := gitlab.Print(e)
		if err != nil {
			t.Skip()
		}
		got, err := gitlab.Parse(s)
		if err != nil || !reflect.DeepEqual(got, e) {
			t.Fatalf("Parse(Print(%#v)) = %#v, %v (text %q)", e, got, err, s)
		}
	})
}

func FuzzParse(f *testing.F) {
	for _, s := range []string{`$A == "x"`, `$A =~ /a\/b/i && !$B`, `($A || $B) && $C != null`, `$[[ inputs.x ]]`, `$A =~ $B`} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, src string) {
		e, err := gitlab.Parse(src)
		if err != nil {
			return
		}
		s, err := gitlab.Print(e)
		if err != nil {
			return
		}
		back, err := gitlab.Parse(s)
		if err != nil {
			t.Fatalf("Print(%#v) = %q does not reparse: %v", e, s, err)
		}
		s2, err := gitlab.Print(back)
		if err != nil || s2 != s {
			t.Fatalf("print not stable: %q then %q (%v)", s, s2, err)
		}
	})
}

// Nesting is one unit per paren or '!' level, however many operators sit at one level.
func TestParseDepthBoundary(t *testing.T) {
	m := expr.MaxDepth
	cases := []struct {
		name string
		src  string
		ok   bool
	}{
		{"parens at max", strings.Repeat("(", m) + "$A" + strings.Repeat(")", m), true},
		{"parens over", strings.Repeat("(", m+1) + "$A" + strings.Repeat(")", m+1), false},
		{"unary at max", strings.Repeat("!", m) + "$A", true},
		{"unary over", strings.Repeat("!", m+1) + "$A", false},
	}
	for _, c := range cases {
		_, err := gitlab.Parse(c.src)
		if (err == nil) != c.ok {
			t.Errorf("%s: err=%v, want ok=%v", c.name, err, c.ok)
		}
	}
}

// A chain longer than MaxDepth parses, validates and survives the canonical text, because a flat run of one
// operator is one nesting level.
func TestLongOperatorChainsAreCheckValid(t *testing.T) {
	for name, src := range map[string]string{
		"flat":  strings.Repeat("$A || ", 200) + "$A",
		"mixed": strings.Repeat("$A == 'x' || $B && $C != 'y' || ", 200) + "$A",
	} {
		e, err := gitlab.Parse(src)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if err := expr.Check(e); err != nil {
			t.Fatalf("%s: Check: %v", name, err)
		}
		back, err := expr.ParseCanonical(expr.String(e))
		if err != nil || !reflect.DeepEqual(e, back) {
			t.Fatalf("%s: canonical round trip failed: %v", name, err)
		}
	}
}
